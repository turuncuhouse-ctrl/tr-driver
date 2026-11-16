package net.neciparmagan.trdriver.calls

import android.content.Context
import android.content.Intent
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.SessionStore
import java.util.UUID

/**
 * Central coordinator connecting [CallStateMachine], [LocalTelCallProvider],
 * [AndroidContactDirectory], [CallAudioController], [TelecomHelper], and
 * [TrCallService].
 */
object CallController {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    private val _callState = MutableStateFlow(CallStateMachine.idle())
    val callState: StateFlow<CallSessionState> = _callState.asStateFlow()

    private val _providerStatus = MutableStateFlow(ProviderStatusSnapshot())
    val providerStatus: StateFlow<ProviderStatusSnapshot> = _providerStatus.asStateFlow()

    @Volatile
    private var appContext: Context? = null

    @Volatile
    private var activeProvider: LocalTelCallProvider? = null

    @Volatile
    private var activeProviderKey: String = ""

    @Volatile
    private var audioController: CallAudioController? = null

    private var eventJob: Job? = null
    private var statusJob: Job? = null
    private var ringTimeoutJob: Job? = null

    fun currentState(): CallSessionState = _callState.value

    @Synchronized
    fun init(context: Context) {
        if (appContext == null) {
            appContext = context.applicationContext
            audioController = CallAudioController(context.applicationContext)
            CallNotifications.ensureChannels(context.applicationContext)
            TelecomHelper.ensurePhoneAccount(context.applicationContext)
        }
    }

    @Synchronized
    fun ensureProviderStarted(context: Context, forceRestart: Boolean = false) {
        init(context)
        val ctx = context.applicationContext
        val session = SessionStore(ctx)
        val cfg = session.loadLocalTelConfig()
        if (!cfg.isReady(session.serverUrl)) {
            disconnectProvider()
            return
        }

        val key = "${cfg.providerType.wireValue}|${cfg.serverUrl}|${cfg.sipDomain}|${cfg.sipPort}|${cfg.transport.wireValue}|${cfg.username}"
        if (!forceRestart &&
            activeProvider != null &&
            activeProviderKey == key &&
            _providerStatus.value.state == ProviderRegistrationState.REGISTERED
        ) {
            return
        }

        val existing = activeProvider
        val needsReplace = existing == null || existing.providerId != cfg.providerType.wireValue
        val provider = if (needsReplace) {
            eventJob?.cancel()
            statusJob?.cancel()
            scope.launch { runCatching { existing?.stop() } }
            when (cfg.providerType) {
                LocalTelProviderType.GATEWAY -> GatewayLocalTelProvider(ctx, session)
                LocalTelProviderType.SIP -> SipLocalTelProvider(ctx)
            }.also { created ->
                activeProvider = created
                observeProvider(created)
            }
        } else {
            existing!!
        }

        activeProviderKey = key
        scope.launch {
            provider.start(cfg)
        }
    }

    @Synchronized
    fun disconnectProvider() {
        val prev = activeProvider
        activeProvider = null
        activeProviderKey = ""
        eventJob?.cancel()
        statusJob?.cancel()
        _providerStatus.value = ProviderStatusSnapshot(
            state = ProviderRegistrationState.DISABLED,
            statusText = "Kapalı",
        )
        if (prev != null) {
            scope.launch { runCatching { prev.stop() } }
        }
    }

    fun startOutgoingCall(
        context: Context,
        rawTarget: String,
        displayNameOverride: String = "",
    ): Result<String> {
        init(context)
        val ctx = context.applicationContext
        val session = SessionStore(ctx)
        val cfg = session.loadLocalTelConfig()
        if (!cfg.isReady(session.serverUrl)) {
            return Result.failure(IllegalStateException("Önce LocalTel hesap ayarlarını kaydedin"))
        }
        if (_callState.value.isLive) {
            return Result.failure(IllegalStateException("Şu anda devam eden başka bir arama var"))
        }
        val norm = PhoneNumberNormalizer.normalize(rawTarget, cfg.defaultCountryCode)
        if (!norm.isDialable) {
            return Result.failure(IllegalArgumentException("Geçerli bir numara veya dahili girin"))
        }

        val party = AndroidContactDirectory.resolveParty(
            context = ctx,
            rawTarget = rawTarget,
            fallbackDisplayName = displayNameOverride,
            defaultCountryCode = cfg.defaultCountryCode,
        )

        val callId = "call_${UUID.randomUUID().toString().take(12)}"
        val transition = CallStateMachine.reduce(
            _callState.value,
            CallEvent.StartOutgoing(
                callId = callId,
                remoteAddress = norm.e164 ?: norm.extensionKey ?: norm.sipUser ?: rawTarget.trim(),
                remoteDisplayName = party.displayName,
                matchedFromContacts = party.matchedFromContacts,
                contactLabel = party.contactLabel,
                nowMs = System.currentTimeMillis(),
            ),
        )
        if (!transition.accepted) {
            return Result.failure(IllegalStateException("Arama başlatılamadı"))
        }
        _callState.value = transition.state

        audioController?.enterInCommunicationAudio(speakerOn = false)
        audioController?.updateProximityLock(true)
        TelecomHelper.reportOutgoingCall(ctx, callId, transition.state.remoteAddress, party.displayName)
        TrCallService.sync(ctx)
        launchCallScreen(ctx, callId)
        armRingTimeout(callId, 60_000L)

        ensureProviderStarted(ctx)
        val provider = activeProvider
        if (provider == null) {
            handleCallEnd(ctx, callId, CallEndReason.CONFIG_ERROR, "LocalTel sağlayıcısı başlatılamadı")
            return Result.failure(IllegalStateException("LocalTel sağlayıcısı hazır değil"))
        }

        scope.launch {
            if (_providerStatus.value.state != ProviderRegistrationState.REGISTERED &&
                cfg.providerType == LocalTelProviderType.GATEWAY
            ) {
                delay(500)
            }
            val dialString = norm.e164 ?: norm.extensionKey ?: norm.sipUser ?: rawTarget.trim()
            val res = provider.startOutgoingCall(callId, dialString, cfg)
            if (res.isFailure) {
                handleCallEnd(
                    ctx,
                    callId,
                    CallEndReason.NETWORK_ERROR,
                    res.exceptionOrNull()?.message ?: "Arama başlatılamadı",
                )
            }
        }

        return Result.success(callId)
    }

    /**
     * Triggers a simulated local incoming call (for lock-screen UI & audio
     * verification without waiting for an external PBX ring).
     */
    fun triggerTestIncomingCall(context: Context, rawNumber: String, fallbackName: String = "LocalTel Test") {
        init(context)
        val callId = "test_${UUID.randomUUID().toString().take(10)}"
        handleIncomingFromProvider(
            context.applicationContext,
            CallProviderEvent.IncomingCall(
                callId = callId,
                remoteUri = rawNumber,
                displayNameHint = fallbackName,
            ),
        )
    }

    fun onTelecomShowIncomingUi(context: Context, callId: String) {
        val state = _callState.value
        if (!state.isLive || state.callId != callId) return
        TrCallService.sync(context.applicationContext)
        launchCallScreen(context.applicationContext, callId)
    }

    fun acceptIncomingCall(context: Context, callId: String) {
        init(context)
        val ctx = context.applicationContext
        val targetId = callId.ifBlank { _callState.value.callId }
        val res = CallStateMachine.reduce(
            _callState.value,
            CallEvent.UserAccept(targetId, System.currentTimeMillis()),
        )
        if (!res.accepted) return
        _callState.value = res.state
        ringTimeoutJob?.cancel()

        audioController?.stopRinging()
        audioController?.enterInCommunicationAudio(speakerOn = res.state.speakerOn)
        audioController?.updateProximityLock(res.state.shouldAcquireProximityLock)
        TrCallService.sync(ctx)
        launchCallScreen(ctx, targetId)

        if (targetId.startsWith("test_")) {
            val activeRes = CallStateMachine.reduce(
                _callState.value,
                CallEvent.MediaConnected(targetId, System.currentTimeMillis()),
            )
            if (activeRes.accepted) {
                _callState.value = activeRes.state
                TelecomHelper.markActive(targetId)
                TrCallService.sync(ctx)
            }
            return
        }

        scope.launch {
            val acceptRes = activeProvider?.acceptIncomingCall(targetId)
            if (acceptRes?.isFailure == true) {
                handleCallEnd(
                    ctx,
                    targetId,
                    CallEndReason.NETWORK_ERROR,
                    acceptRes.exceptionOrNull()?.message ?: "Çağrı yanıtlanamadı",
                )
            }
        }
    }

    fun rejectIncomingCall(context: Context, callId: String) {
        init(context)
        val ctx = context.applicationContext
        val targetId = callId.ifBlank { _callState.value.callId }
        val res = CallStateMachine.reduce(
            _callState.value,
            CallEvent.UserReject(targetId, System.currentTimeMillis()),
        )
        if (!res.accepted) {
            TrCallService.stop(ctx)
            return
        }
        _callState.value = res.state
        if (!targetId.startsWith("test_")) {
            scope.launch { activeProvider?.rejectIncomingCall(targetId) }
        }
        performCompleteCleanup(ctx, targetId, CallEndReason.REJECTED)
    }

    fun hangupCall(context: Context, callId: String) {
        init(context)
        val ctx = context.applicationContext
        val targetId = callId.ifBlank { _callState.value.callId }
        val res = CallStateMachine.reduce(
            _callState.value,
            CallEvent.UserHangup(targetId, System.currentTimeMillis()),
        )
        if (!res.accepted) {
            performCompleteCleanup(ctx, targetId, CallEndReason.LOCAL_HANGUP)
            return
        }
        _callState.value = res.state
        if (!targetId.startsWith("test_")) {
            scope.launch { activeProvider?.hangupCall(targetId) }
        }
        performCompleteCleanup(ctx, targetId, res.state.endReason)
    }

    fun toggleMute(context: Context) {
        val state = _callState.value
        if (!state.isLive) return
        val next = !state.muted
        val res = CallStateMachine.reduce(_callState.value, CallEvent.SetMuted(state.callId, next))
        if (!res.accepted) return
        _callState.value = res.state
        audioController?.setMicrophoneMute(next)
        scope.launch { activeProvider?.setMuted(state.callId, next) }
        TrCallService.sync(context.applicationContext)
    }

    fun syncSystemMute(callId: String, muted: Boolean) {
        val state = _callState.value
        if (!state.isLive || state.callId != callId) return
        val res = CallStateMachine.reduce(state, CallEvent.SetMuted(callId, muted))
        if (res.accepted) {
            _callState.value = res.state
            scope.launch { activeProvider?.setMuted(callId, muted) }
        }
    }

    fun toggleSpeaker(context: Context) {
        val state = _callState.value
        if (!state.isLive) return
        val next = !state.speakerOn
        val res = CallStateMachine.reduce(_callState.value, CallEvent.SetSpeaker(state.callId, next))
        if (!res.accepted) return
        _callState.value = res.state
        audioController?.setSpeakerphone(next)
        audioController?.updateProximityLock(res.state.shouldAcquireProximityLock)
        TrCallService.sync(context.applicationContext)
    }

    fun setHold(context: Context, callId: String, onHold: Boolean) {
        val state = _callState.value
        if (!state.isLive || state.callId != callId) return
        val res = CallStateMachine.reduce(state, CallEvent.SetHold(callId, onHold))
        if (res.accepted) {
            _callState.value = res.state
            audioController?.setMicrophoneMute(onHold || res.state.muted)
            TrCallService.sync(context.applicationContext)
        }
    }

    fun sendDtmf(digit: Char) {
        val state = _callState.value
        if (!state.isLive) return
        scope.launch { activeProvider?.sendDtmf(state.callId, digit) }
    }

    private fun observeProvider(provider: LocalTelCallProvider) {
        statusJob?.cancel()
        statusJob = scope.launch {
            provider.statusFlow.collect { _providerStatus.value = it }
        }
        eventJob?.cancel()
        eventJob = scope.launch {
            provider.events.collect { ev ->
                val ctx = appContext ?: return@collect
                when (ev) {
                    is CallProviderEvent.IncomingCall -> handleIncomingFromProvider(ctx, ev)
                    is CallProviderEvent.OutgoingRinging -> {
                        val res = CallStateMachine.reduce(
                            _callState.value,
                            CallEvent.RemoteRinging(ev.callId),
                        )
                        if (res.accepted) {
                            _callState.value = res.state
                            audioController?.startOutgoingRingback()
                            TrCallService.sync(ctx)
                        }
                    }
                    is CallProviderEvent.CallConnected -> {
                        val base = _callState.value
                        val interim = if (base.phase == CallPhase.OUTGOING_DIALING ||
                            base.phase == CallPhase.OUTGOING_RINGING
                        ) {
                            CallStateMachine.reduce(base, CallEvent.RemoteAccept(ev.callId, System.currentTimeMillis())).state
                        } else {
                            base
                        }
                        val res = CallStateMachine.reduce(
                            interim,
                            CallEvent.MediaConnected(ev.callId, System.currentTimeMillis()),
                        )
                        if (res.accepted) {
                            _callState.value = res.state
                            ringTimeoutJob?.cancel()
                            audioController?.stopRingback()
                            audioController?.stopRinging()
                            audioController?.enterInCommunicationAudio(res.state.speakerOn)
                            audioController?.updateProximityLock(res.state.shouldAcquireProximityLock)
                            TelecomHelper.markActive(ev.callId)
                            TrCallService.sync(ctx)
                        }
                    }
                    is CallProviderEvent.CallEnded -> {
                        handleCallEnd(ctx, ev.callId, ev.reason, "")
                    }
                    is CallProviderEvent.CallError -> {
                        handleCallEnd(ctx, ev.callId, ev.reason, ev.message)
                    }
                    is CallProviderEvent.CallMissed -> {
                        val current = _callState.value
                        if (current.callId == ev.callId && current.phase == CallPhase.INCOMING_RINGING) {
                            // Still ringing this exact call: end it as no-answer
                            // (handleCallEnd raises the missed notification).
                            handleCallEnd(ctx, ev.callId, CallEndReason.NO_ANSWER, "")
                        } else if (!current.isLive || current.callId != ev.callId) {
                            // Already gone (or the call timed out while this
                            // phone was offline): show the missed call directly.
                            val missed = CallSessionState(
                                callId = ev.callId,
                                direction = CallDirection.INCOMING,
                                phase = CallPhase.ENDED,
                                remoteAddress = ev.remoteUri,
                                remoteDisplayName = ev.displayNameHint,
                                startedAtMs = System.currentTimeMillis(),
                                endedAtMs = System.currentTimeMillis(),
                                endReason = CallEndReason.NO_ANSWER,
                            )
                            CallNotifications.notifyMissedCall(ctx, missed)
                        }
                    }
                }
            }
        }
    }

    private fun handleIncomingFromProvider(context: Context, ev: CallProviderEvent.IncomingCall) {
        if (_callState.value.isLive) {
            scope.launch { activeProvider?.rejectIncomingCall(ev.callId) }
            return
        }
        val session = SessionStore(context)
        val cfg = session.loadLocalTelConfig()
        val party = AndroidContactDirectory.resolveParty(
            context = context,
            rawTarget = ev.remoteUri,
            fallbackDisplayName = ev.displayNameHint,
            defaultCountryCode = cfg.defaultCountryCode,
        )
        val address = party.normalized.e164
            ?: party.normalized.extensionKey
            ?: party.normalized.sipUser
            ?: ev.remoteUri

        val res = CallStateMachine.reduce(
            _callState.value,
            CallEvent.ReceiveIncoming(
                callId = ev.callId,
                remoteAddress = address,
                remoteDisplayName = party.displayName,
                matchedFromContacts = party.matchedFromContacts,
                contactLabel = party.contactLabel,
                nowMs = System.currentTimeMillis(),
            ),
        )
        if (!res.accepted) return
        _callState.value = res.state

        audioController?.startIncomingRinging()
        TelecomHelper.reportIncomingCall(
            context,
            ev.callId,
            address,
            party.displayName,
        )
        // Post the full-screen incoming notification directly as well, so the
        // call still shows on a locked/idle phone even if starting the call
        // foreground service from the background is refused by the system.
        runCatching {
            val nm = context.getSystemService(android.app.NotificationManager::class.java)
            nm?.notify(CallNotifications.CALL_NOTIFICATION_ID, CallNotifications.buildCallNotification(context, res.state))
        }
        TrCallService.sync(context)
        // Always attempt to present lock-screen UI immediately; fullScreenIntent
        // on TrCallService handles locked devices on Android 10+.
        launchCallScreen(context, ev.callId)
        armRingTimeout(ev.callId, 45_000L)
    }

    private fun armRingTimeout(callId: String, timeoutMs: Long) {
        ringTimeoutJob?.cancel()
        ringTimeoutJob = scope.launch {
            delay(timeoutMs)
            val state = _callState.value
            if (state.callId == callId &&
                (state.phase == CallPhase.INCOMING_RINGING ||
                    state.phase == CallPhase.OUTGOING_DIALING ||
                    state.phase == CallPhase.OUTGOING_RINGING)
            ) {
                val ctx = appContext ?: return@launch
                if (!callId.startsWith("test_")) {
                    if (state.phase == CallPhase.INCOMING_RINGING) {
                        runCatching { activeProvider?.rejectIncomingCall(callId) }
                    } else {
                        runCatching { activeProvider?.hangupCall(callId) }
                    }
                }
                val wasIncoming = state.phase == CallPhase.INCOMING_RINGING
                val res = CallStateMachine.reduce(
                    state,
                    CallEvent.RingTimeout(callId, System.currentTimeMillis()),
                )
                if (res.accepted) {
                    _callState.value = res.state
                    if (wasIncoming) {
                        CallNotifications.notifyMissedCall(ctx, res.state)
                    }
                    performCompleteCleanup(ctx, callId, CallEndReason.NO_ANSWER)
                }
            }
        }
    }

    private fun handleCallEnd(context: Context, callId: String, reason: CallEndReason, message: String) {
        val current = _callState.value
        if (current.callId != callId || !current.isLive) return
        val wasUnansweredIncoming = current.phase == CallPhase.INCOMING_RINGING &&
            (reason == CallEndReason.CANCELLED || reason == CallEndReason.NO_ANSWER)
        val ev = if (message.isNotBlank()) {
            CallEvent.CallFailed(callId, reason, message, System.currentTimeMillis())
        } else {
            CallEvent.RemoteHangup(callId, reason, System.currentTimeMillis())
        }
        val res = CallStateMachine.reduce(current, ev)
        if (res.accepted) {
            _callState.value = res.state
            if (wasUnansweredIncoming) {
                CallNotifications.notifyMissedCall(context, res.state)
            }
            performCompleteCleanup(context, callId, reason)
        }
    }

    private fun performCompleteCleanup(context: Context, callId: String, reason: CallEndReason) {
        ringTimeoutJob?.cancel()
        ringTimeoutJob = null
        audioController?.releaseAll()
        TelecomHelper.markEnded(callId, reason)
        TrCallService.stop(context)
        scope.launch {
            delay(1_100L)
            if (_callState.value.callId == callId && _callState.value.isTerminal) {
                _callState.value = CallStateMachine.idle()
            }
        }
    }

    private fun launchCallScreen(context: Context, callId: String) {
        val intent = Intent(context, CallActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or
                Intent.FLAG_ACTIVITY_SINGLE_TOP or
                Intent.FLAG_ACTIVITY_REORDER_TO_FRONT
            putExtra(CallActivity.EXTRA_CALL_ID, callId)
        }
        runCatching { context.startActivity(intent) }
    }
}
