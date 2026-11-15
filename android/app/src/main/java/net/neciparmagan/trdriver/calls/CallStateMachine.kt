package net.neciparmagan.trdriver.calls

enum class CallDirection {
    INCOMING,
    OUTGOING,
}

enum class CallPhase {
    IDLE,
    OUTGOING_DIALING,
    OUTGOING_RINGING,
    INCOMING_RINGING,
    CONNECTING,
    ACTIVE,
    ENDING,
    ENDED,
}

enum class CallEndReason {
    NONE,
    LOCAL_HANGUP,
    REMOTE_HANGUP,
    REJECTED,
    CANCELLED,
    BUSY,
    NO_ANSWER,
    NETWORK_ERROR,
    CONFIG_ERROR,
    AUDIO_ERROR,
}

data class CallSessionState(
    val callId: String = "",
    val direction: CallDirection = CallDirection.OUTGOING,
    val phase: CallPhase = CallPhase.IDLE,
    val remoteAddress: String = "",
    val remoteDisplayName: String = "",
    val matchedFromContacts: Boolean = false,
    val contactLabel: String = "",
    val muted: Boolean = false,
    val speakerOn: Boolean = false,
    val onHold: Boolean = false,
    val startedAtMs: Long = 0L,
    val connectedAtMs: Long = 0L,
    val endedAtMs: Long = 0L,
    val endReason: CallEndReason = CallEndReason.NONE,
    val errorMessage: String = "",
) {
    val isLive: Boolean
        get() = phase != CallPhase.IDLE && phase != CallPhase.ENDED

    val isTerminal: Boolean
        get() = phase == CallPhase.ENDED

    val shouldRing: Boolean
        get() = phase == CallPhase.INCOMING_RINGING

    val shouldPlayRingback: Boolean
        get() = phase == CallPhase.OUTGOING_RINGING

    val shouldShowLockScreenUi: Boolean
        get() = phase == CallPhase.INCOMING_RINGING ||
            phase == CallPhase.OUTGOING_DIALING ||
            phase == CallPhase.OUTGOING_RINGING ||
            phase == CallPhase.CONNECTING ||
            phase == CallPhase.ACTIVE

    val shouldHoldAudioFocus: Boolean
        get() = phase == CallPhase.INCOMING_RINGING ||
            phase == CallPhase.OUTGOING_DIALING ||
            phase == CallPhase.OUTGOING_RINGING ||
            phase == CallPhase.CONNECTING ||
            phase == CallPhase.ACTIVE

    val shouldAcquireProximityLock: Boolean
        get() = (phase == CallPhase.OUTGOING_RINGING ||
            phase == CallPhase.CONNECTING ||
            phase == CallPhase.ACTIVE) && !speakerOn

    val canAnswer: Boolean
        get() = phase == CallPhase.INCOMING_RINGING

    val canReject: Boolean
        get() = phase == CallPhase.INCOMING_RINGING

    val canHangup: Boolean
        get() = phase == CallPhase.OUTGOING_DIALING ||
            phase == CallPhase.OUTGOING_RINGING ||
            phase == CallPhase.INCOMING_RINGING ||
            phase == CallPhase.CONNECTING ||
            phase == CallPhase.ACTIVE ||
            phase == CallPhase.ENDING

    val canToggleAudioControls: Boolean
        get() = phase == CallPhase.OUTGOING_DIALING ||
            phase == CallPhase.OUTGOING_RINGING ||
            phase == CallPhase.CONNECTING ||
            phase == CallPhase.ACTIVE

    fun elapsedMs(nowMs: Long): Long {
        if (connectedAtMs <= 0L) return 0L
        val end = if (endedAtMs > 0L) endedAtMs else nowMs
        return (end - connectedAtMs).coerceAtLeast(0L)
    }

    /** Never include phone numbers or display names in logs. */
    override fun toString(): String =
        "CallSessionState(callId=$callId, direction=$direction, phase=$phase, matched=$matchedFromContacts, endReason=$endReason)"
}

sealed class CallEvent {
    data class StartOutgoing(
        val callId: String,
        val remoteAddress: String,
        val remoteDisplayName: String,
        val matchedFromContacts: Boolean,
        val contactLabel: String = "",
        val nowMs: Long = 0L,
    ) : CallEvent()

    data class ReceiveIncoming(
        val callId: String,
        val remoteAddress: String,
        val remoteDisplayName: String,
        val matchedFromContacts: Boolean,
        val contactLabel: String = "",
        val nowMs: Long = 0L,
    ) : CallEvent()

    data class RemoteRinging(val callId: String) : CallEvent()
    data class UserAccept(val callId: String, val nowMs: Long = 0L) : CallEvent()
    data class RemoteAccept(val callId: String, val nowMs: Long = 0L) : CallEvent()
    data class MediaConnected(val callId: String, val nowMs: Long = 0L) : CallEvent()
    data class UserReject(val callId: String, val nowMs: Long = 0L) : CallEvent()
    data class UserHangup(val callId: String, val nowMs: Long = 0L) : CallEvent()
    data class RemoteHangup(
        val callId: String,
        val reason: CallEndReason = CallEndReason.REMOTE_HANGUP,
        val nowMs: Long = 0L,
    ) : CallEvent()

    data class RingTimeout(val callId: String, val nowMs: Long = 0L) : CallEvent()
    data class CallFailed(
        val callId: String,
        val reason: CallEndReason = CallEndReason.NETWORK_ERROR,
        val message: String = "",
        val nowMs: Long = 0L,
    ) : CallEvent()

    data class SetMuted(val callId: String, val muted: Boolean) : CallEvent()
    data class SetSpeaker(val callId: String, val speakerOn: Boolean) : CallEvent()
    data class SetHold(val callId: String, val onHold: Boolean) : CallEvent()
    data class CleanupCompleted(val callId: String, val nowMs: Long = 0L) : CallEvent()
}

data class CallTransitionResult(
    val state: CallSessionState,
    val accepted: Boolean,
    val rejectionReason: String? = null,
    val cleanupRequired: Boolean = false,
)

object CallStateMachine {
    fun idle(): CallSessionState = CallSessionState()

    fun reduce(current: CallSessionState?, event: CallEvent): CallTransitionResult {
        val base = current ?: CallSessionState()

        return when (event) {
            is CallEvent.StartOutgoing -> {
                if (base.isLive) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "already_in_call")
                }
                if (event.callId.isBlank() || event.remoteAddress.isBlank()) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "invalid_target")
                }
                CallTransitionResult(
                    state = CallSessionState(
                        callId = event.callId,
                        direction = CallDirection.OUTGOING,
                        phase = CallPhase.OUTGOING_DIALING,
                        remoteAddress = event.remoteAddress,
                        remoteDisplayName = event.remoteDisplayName.ifBlank { event.remoteAddress },
                        matchedFromContacts = event.matchedFromContacts,
                        contactLabel = event.contactLabel,
                        startedAtMs = event.nowMs,
                    ),
                    accepted = true,
                )
            }

            is CallEvent.ReceiveIncoming -> {
                if (base.isLive) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "busy")
                }
                if (event.callId.isBlank() || event.remoteAddress.isBlank()) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "invalid_caller")
                }
                CallTransitionResult(
                    state = CallSessionState(
                        callId = event.callId,
                        direction = CallDirection.INCOMING,
                        phase = CallPhase.INCOMING_RINGING,
                        remoteAddress = event.remoteAddress,
                        remoteDisplayName = event.remoteDisplayName.ifBlank { event.remoteAddress },
                        matchedFromContacts = event.matchedFromContacts,
                        contactLabel = event.contactLabel,
                        startedAtMs = event.nowMs,
                    ),
                    accepted = true,
                )
            }

            is CallEvent.RemoteRinging -> {
                if (!matchesLiveCall(base, event.callId) || base.phase != CallPhase.OUTGOING_DIALING) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "invalid_phase")
                }
                CallTransitionResult(base.copy(phase = CallPhase.OUTGOING_RINGING), accepted = true)
            }

            is CallEvent.UserAccept -> {
                if (!matchesLiveCall(base, event.callId) || base.phase != CallPhase.INCOMING_RINGING) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "cannot_accept")
                }
                CallTransitionResult(
                    state = base.copy(phase = CallPhase.CONNECTING),
                    accepted = true,
                )
            }

            is CallEvent.RemoteAccept -> {
                if (!matchesLiveCall(base, event.callId) ||
                    (base.phase != CallPhase.OUTGOING_DIALING && base.phase != CallPhase.OUTGOING_RINGING)
                ) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "cannot_remote_accept")
                }
                CallTransitionResult(
                    state = base.copy(phase = CallPhase.CONNECTING),
                    accepted = true,
                )
            }

            is CallEvent.MediaConnected -> {
                if (!matchesLiveCall(base, event.callId) ||
                    (base.phase != CallPhase.CONNECTING &&
                        base.phase != CallPhase.OUTGOING_RINGING &&
                        base.phase != CallPhase.OUTGOING_DIALING)
                ) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "cannot_activate")
                }
                val connectedTs = if (base.connectedAtMs > 0L) base.connectedAtMs else event.nowMs
                CallTransitionResult(
                    state = base.copy(phase = CallPhase.ACTIVE, connectedAtMs = connectedTs),
                    accepted = true,
                )
            }

            is CallEvent.UserReject -> {
                if (!matchesLiveCall(base, event.callId) || base.phase != CallPhase.INCOMING_RINGING) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "cannot_reject")
                }
                terminate(base, CallEndReason.REJECTED, "", event.nowMs)
            }

            is CallEvent.UserHangup -> {
                if (!matchesLiveCall(base, event.callId) || !base.canHangup) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "not_live")
                }
                val reason = if (base.direction == CallDirection.OUTGOING &&
                    (base.phase == CallPhase.OUTGOING_DIALING || base.phase == CallPhase.OUTGOING_RINGING)
                ) {
                    CallEndReason.CANCELLED
                } else {
                    CallEndReason.LOCAL_HANGUP
                }
                terminate(base, reason, "", event.nowMs)
            }

            is CallEvent.RemoteHangup -> {
                if (!matchesLiveCall(base, event.callId) || !base.isLive) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "not_live")
                }
                terminate(base, event.reason, "", event.nowMs)
            }

            is CallEvent.RingTimeout -> {
                if (!matchesLiveCall(base, event.callId) ||
                    (base.phase != CallPhase.INCOMING_RINGING &&
                        base.phase != CallPhase.OUTGOING_DIALING &&
                        base.phase != CallPhase.OUTGOING_RINGING)
                ) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "not_ringing")
                }
                terminate(base, CallEndReason.NO_ANSWER, "Cevap verilmedi", event.nowMs)
            }

            is CallEvent.CallFailed -> {
                if (!matchesLiveCall(base, event.callId) || base.phase == CallPhase.ENDED) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "not_live")
                }
                terminate(base, event.reason, event.message, event.nowMs)
            }

            is CallEvent.SetMuted -> {
                if (!matchesLiveCall(base, event.callId) || !base.canToggleAudioControls) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "invalid_phase")
                }
                CallTransitionResult(base.copy(muted = event.muted), accepted = true)
            }

            is CallEvent.SetSpeaker -> {
                if (!matchesLiveCall(base, event.callId) || !base.canToggleAudioControls) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "invalid_phase")
                }
                CallTransitionResult(base.copy(speakerOn = event.speakerOn), accepted = true)
            }

            is CallEvent.SetHold -> {
                if (!matchesLiveCall(base, event.callId) || base.phase != CallPhase.ACTIVE) {
                    return CallTransitionResult(base, accepted = false, rejectionReason = "not_active")
                }
                CallTransitionResult(base.copy(onHold = event.onHold), accepted = true)
            }

            is CallEvent.CleanupCompleted -> {
                if (base.callId != event.callId || base.phase == CallPhase.ENDED) {
                    return CallTransitionResult(base, accepted = false)
                }
                CallTransitionResult(
                    state = base.copy(
                        phase = CallPhase.ENDED,
                        muted = false,
                        speakerOn = false,
                        onHold = false,
                        endedAtMs = if (base.endedAtMs > 0L) base.endedAtMs else event.nowMs,
                    ),
                    accepted = true,
                    cleanupRequired = false,
                )
            }
        }
    }

    private fun matchesLiveCall(state: CallSessionState, callId: String): Boolean =
        state.callId.isNotBlank() && state.callId == callId

    private fun terminate(
        base: CallSessionState,
        reason: CallEndReason,
        message: String,
        nowMs: Long,
    ): CallTransitionResult {
        return CallTransitionResult(
            state = base.copy(
                phase = CallPhase.ENDED,
                muted = false,
                speakerOn = false,
                onHold = false,
                endedAtMs = nowMs,
                endReason = reason,
                errorMessage = message,
            ),
            accepted = true,
            cleanupRequired = true,
        )
    }

    fun formatDuration(durationMs: Long): String {
        val totalSec = (durationMs.coerceAtLeast(0L) / 1000L)
        val hours = totalSec / 3600L
        val minutes = (totalSec % 3600L) / 60L
        val seconds = totalSec % 60L
        return if (hours > 0L) {
            String.format("%d:%02d:%02d", hours, minutes, seconds)
        } else {
            String.format("%02d:%02d", minutes, seconds)
        }
    }
}
