package net.neciparmagan.trdriver.calls

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.io.OutputStream
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.Inet4Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.security.SecureRandom
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicInteger
import javax.net.ssl.SSLSocketFactory

/**
 * Direct RFC 3261 SIP + Digest + SDP + G.711 RTP implementation of [LocalTelCallProvider].
 * Supports UDP, TCP, and TLS transports without logging credentials, URIs, or numbers.
 */
class SipLocalTelProvider(private val context: Context) : LocalTelCallProvider {
    override val providerId: String = "sip"

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val _status = MutableStateFlow(ProviderStatusSnapshot())
    override val statusFlow: StateFlow<ProviderStatusSnapshot> = _status.asStateFlow()

    private val _events = MutableSharedFlow<CallProviderEvent>(extraBufferCapacity = 32)
    override val events: SharedFlow<CallProviderEvent> = _events.asSharedFlow()

    private val cseqCounter = AtomicInteger(1)
    private val random = SecureRandom()

    private var activeConfig: LocalTelConfig? = null
    private var serverInet: InetAddress? = null
    private var serverPort: Int = 5060
    private var localIp: String = "0.0.0.0"
    private var localSipPort: Int = 5060

    private var udpSocket: DatagramSocket? = null
    private var tcpSocket: Socket? = null
    private var tcpOut: OutputStream? = null

    private var readerJob: Job? = null
    private var registerLoopJob: Job? = null
    private var registerCallId: String = ""
    private var registerFromTag: String = ""
    private var lastRegisterCSeq: Int = 0
    private var registerAuthRetried = false

    private data class ActiveDialog(
        val appCallId: String,
        val sipCallId: String,
        val isOutgoing: Boolean,
        var localTag: String,
        var remoteTag: String = "",
        var remoteUri: String,
        var targetUri: String,
        var cseq: Int,
        var inviteCseq: Int,
        var inviteBranch: String,
        var authRetried: Boolean = false,
        var inboundInvite: SipMessage? = null,
        var remoteRtp: RemoteRtpEndpoint? = null,
        var established: Boolean = false,
    )

    private val dialogsByAppId = ConcurrentHashMap<String, ActiveDialog>()
    private val appIdBySipId = ConcurrentHashMap<String, String>()
    private var rtpEngine: RtpAudioEngine? = null

    override suspend fun start(config: LocalTelConfig): Result<Unit> = withContext(Dispatchers.IO) {
        stop()
        if (!config.isConfiguredForSip()) {
            _status.value = ProviderStatusSnapshot(
                state = ProviderRegistrationState.ERROR,
                statusText = "SIP sunucu veya kullanıcı adı eksik",
                lastError = "Eksik yapılandırma",
            )
            return@withContext Result.failure(IllegalStateException("SIP yapılandırması eksik"))
        }

        _status.value = ProviderStatusSnapshot(
            state = ProviderRegistrationState.REGISTERING,
            statusText = "LocalTel bağlanıyor...",
        )

        return@withContext runCatching {
            val host = config.effectiveHost()
            val allAddrs = InetAddress.getAllByName(host)
            val resolved = allAddrs.firstOrNull { it is Inet4Address } ?: InetAddress.getByName(host)
            val port = config.sipPort.coerceIn(1, 65535)
            serverInet = resolved
            serverPort = port
            activeConfig = config
            registerCallId = "${UUID.randomUUID()}@trdriver"
            registerFromTag = randomToken(10)
            registerAuthRetried = false

            when (config.transport) {
                LocalTelTransport.TCP -> {
                    val sock = Socket()
                    CallNetwork.bind(sock)
                    sock.connect(InetSocketAddress(resolved, port), 10_000)
                    sock.soTimeout = 0
                    tcpSocket = sock
                    tcpOut = sock.getOutputStream()
                    localIp = sock.localAddress?.hostAddress ?: "0.0.0.0"
                    localSipPort = sock.localPort
                    startStreamReader(sock.getInputStream())
                }
                LocalTelTransport.TLS -> {
                    val sock = SSLSocketFactory.getDefault().createSocket()
                    CallNetwork.bind(sock)
                    sock.connect(InetSocketAddress(resolved, port), 10_000)
                    sock.soTimeout = 0
                    tcpSocket = sock
                    tcpOut = sock.getOutputStream()
                    localIp = sock.localAddress?.hostAddress ?: "0.0.0.0"
                    localSipPort = sock.localPort
                    startStreamReader(sock.getInputStream())
                }
                else -> {
                    val sock = DatagramSocket(0)
                    CallNetwork.bind(sock)
                    sock.soTimeout = 0
                    // Probe routing interface IP without binding socket strictly.
                    runCatching {
                        DatagramSocket().use { probe ->
                            CallNetwork.bind(probe)
                            probe.connect(resolved, port)
                            localIp = probe.localAddress?.hostAddress ?: "0.0.0.0"
                        }
                    }
                    udpSocket = sock
                    localSipPort = sock.localPort
                    startUdpReader(sock)
                }
            }

            sendRegister(config, authHeaderName = null, authHeaderValue = null)
            startRegisterLoop(config)
        }.onFailure { err ->
            Log.w(TAG, "SIP provider start failed")
            _status.value = ProviderStatusSnapshot(
                state = ProviderRegistrationState.ERROR,
                statusText = "SIP bağlantı hatası",
                lastError = err.message ?: "Bağlantı kurulamadı",
            )
            stopSocketsOnly()
        }
    }

    override suspend fun stop() = withContext(Dispatchers.IO) {
        registerLoopJob?.cancel()
        registerLoopJob = null
        readerJob?.cancel()
        readerJob = null

        val cfg = activeConfig
        if (cfg != null && (udpSocket != null || tcpSocket != null)) {
            runCatching {
                sendRegister(cfg, authHeaderName = null, authHeaderValue = null, expiresSec = 0)
            }
        }
        dialogsByAppId.clear()
        appIdBySipId.clear()
        rtpEngine?.stop()
        rtpEngine = null
        stopSocketsOnly()
        activeConfig = null
        _status.value = ProviderStatusSnapshot(
            state = ProviderRegistrationState.DISABLED,
            statusText = "Kapalı",
        )
    }

    override suspend fun startOutgoingCall(
        callId: String,
        targetNumber: String,
        config: LocalTelConfig,
    ): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val activeCfg = activeConfig ?: error("SIP oturumu başlatılmadı")
            val domain = activeCfg.effectiveDomain()
            val cleanedTarget = formatDialTarget(targetNumber, activeCfg.outboundPrefix)
            val targetUri = if (cleanedTarget.startsWith("sip:", ignoreCase = true) ||
                cleanedTarget.startsWith("sips:", ignoreCase = true)
            ) {
                cleanedTarget
            } else if (cleanedTarget.contains('@')) {
                "sip:$cleanedTarget"
            } else {
                "sip:$cleanedTarget@$domain"
            }

            val engine = RtpAudioEngine(context).also { rtpEngine = it }
            val rtpPort = engine.prepareLocalSocket()
            val sdp = SdpCodec.buildAudioOffer(localIp, rtpPort)

            val sipCallId = "${UUID.randomUUID()}@$localIp"
            val fromTag = randomToken(10)
            val cseq = cseqCounter.getAndIncrement()
            val branch = "z9hG4bK${randomToken(12)}"

            val dialog = ActiveDialog(
                appCallId = callId,
                sipCallId = sipCallId,
                isOutgoing = true,
                localTag = fromTag,
                remoteUri = targetUri,
                targetUri = targetUri,
                cseq = cseq,
                inviteCseq = cseq,
                inviteBranch = branch,
            )
            dialogsByAppId[callId] = dialog
            appIdBySipId[sipCallId] = callId

            sendInvite(activeCfg, dialog, sdp, authHeaderName = null, authHeaderValue = null)
        }
    }

    override suspend fun acceptIncomingCall(callId: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val dialog = dialogsByAppId[callId] ?: error("Çağrı bulunamadı")
            val invite = dialog.inboundInvite ?: error("Gelen INVITE bulunamadı")
            val cfg = activeConfig ?: error("SIP oturumu yok")

            val engine = RtpAudioEngine(context).also { rtpEngine = it }
            val rtpPort = engine.prepareLocalSocket()
            val sdp = SdpCodec.buildAudioOffer(localIp, rtpPort)

            val contactUri = "sip:${cfg.username}@$localIp:$localSipPort;transport=${transportParam(cfg.transport)}"
            val okResponse = SipMessageBuilder.buildResponse(
                request = invite,
                statusCode = 200,
                reasonPhrase = "OK",
                toTag = dialog.localTag,
                contactUri = contactUri,
                contentType = "application/sdp",
                body = sdp,
            )
            sendRaw(okResponse)
            dialog.established = true

            dialog.remoteRtp?.let { remote ->
                val pt = selectPreferredCodec(remote.payloadTypes)
                engine.start(remote.ip, remote.port, pt)
            }
            _events.tryEmit(CallProviderEvent.CallConnected(callId))
            Unit
        }
    }

    override suspend fun rejectIncomingCall(callId: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val dialog = dialogsByAppId.remove(callId) ?: return@runCatching Unit
            appIdBySipId.remove(dialog.sipCallId)
            val invite = dialog.inboundInvite ?: return@runCatching Unit
            val resp = SipMessageBuilder.buildResponse(
                request = invite,
                statusCode = 486,
                reasonPhrase = "Busy Here",
                toTag = dialog.localTag,
            )
            sendRaw(resp)
            rtpEngine?.stop()
            rtpEngine = null
        }
    }

    override suspend fun hangupCall(callId: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val dialog = dialogsByAppId.remove(callId)
            if (dialog != null) {
                appIdBySipId.remove(dialog.sipCallId)
                val cfg = activeConfig
                if (cfg != null) {
                    if (dialog.isOutgoing && !dialog.established) {
                        val cancel = SipMessageBuilder.buildRequest(
                            method = "CANCEL",
                            requestUri = dialog.targetUri,
                            viaTransport = transportLabel(cfg.transport),
                            localHost = localIp,
                            localPort = localSipPort,
                            branch = dialog.inviteBranch,
                            fromDisplayName = cfg.displayName,
                            fromUri = "sip:${cfg.username}@${cfg.effectiveDomain()}",
                            fromTag = dialog.localTag,
                            toUri = dialog.remoteUri,
                            toTag = null,
                            callId = dialog.sipCallId,
                            cseq = dialog.inviteCseq,
                        )
                        sendRaw(cancel)
                    } else if (dialog.established) {
                        val nextCSeq = cseqCounter.getAndIncrement()
                        val bye = SipMessageBuilder.buildRequest(
                            method = "BYE",
                            requestUri = dialog.targetUri,
                            viaTransport = transportLabel(cfg.transport),
                            localHost = localIp,
                            localPort = localSipPort,
                            branch = "z9hG4bK${randomToken(12)}",
                            fromDisplayName = cfg.displayName,
                            fromUri = "sip:${cfg.username}@${cfg.effectiveDomain()}",
                            fromTag = dialog.localTag,
                            toUri = dialog.remoteUri,
                            toTag = dialog.remoteTag.ifBlank { null },
                            callId = dialog.sipCallId,
                            cseq = nextCSeq,
                        )
                        sendRaw(bye)
                    }
                }
            }
            rtpEngine?.stop()
            rtpEngine = null
        }
    }

    override suspend fun setMuted(callId: String, muted: Boolean) {
        rtpEngine?.setMuted(muted)
    }

    override suspend fun sendDtmf(callId: String, digit: Char) {
        rtpEngine?.sendDtmf(digit)
    }

    private fun startRegisterLoop(config: LocalTelConfig) {
        registerLoopJob = scope.launch {
            var tick = 0
            while (isActive) {
                delay(25_000L)
                tick++
                if (config.transport == LocalTelTransport.UDP) {
                    // RFC 5626 double-CRLF keepalive keeps NAT mapping open.
                    runCatching { sendRaw("\r\n\r\n") }
                }
                if (tick % 9 == 0) {
                    registerAuthRetried = false
                    runCatching {
                        sendRegister(config, authHeaderName = null, authHeaderValue = null)
                    }
                }
            }
        }
    }

    private fun startUdpReader(sock: DatagramSocket) {
        readerJob = scope.launch {
            val buf = ByteArray(16 * 1024)
            val dp = DatagramPacket(buf, buf.size)
            while (isActive && !sock.isClosed) {
                val ok = runCatching { sock.receive(dp) }.isSuccess
                if (!ok) continue
                val text = String(dp.data, 0, dp.length, Charsets.UTF_8)
                if (text.isBlank()) continue
                SipMessage.parse(text)?.let { handleSipMessage(it) }
            }
        }
    }

    private fun startStreamReader(input: InputStream) {
        readerJob = scope.launch {
            val buffer = ByteArrayOutputStream()
            val chunk = ByteArray(4096)
            while (isActive) {
                val n = runCatching { input.read(chunk) }.getOrDefault(-1)
                if (n <= 0) {
                    _status.value = ProviderStatusSnapshot(
                        state = ProviderRegistrationState.ERROR,
                        statusText = "SIP bağlantısı kesildi",
                        lastError = "Bağlantı kapandı",
                    )
                    break
                }
                buffer.write(chunk, 0, n)
                drainSipFrames(buffer)
            }
        }
    }

    private fun drainSipFrames(buffer: ByteArrayOutputStream) {
        while (true) {
            val data = buffer.toByteArray()
            val text = String(data, Charsets.UTF_8)
            val trimmedStart = text.indexOfFirst { it != '\r' && it != '\n' }
            if (trimmedStart < 0) {
                buffer.reset()
                return
            }
            val headEnd = text.indexOf("\r\n\r\n", trimmedStart)
            if (headEnd < 0) return

            val headersOnly = text.substring(trimmedStart, headEnd)
            val parsedHead = SipMessage.parse("$headersOnly\r\n\r\n")
            val contentLen = parsedHead?.header("Content-Length")?.toIntOrNull()
                ?: parsedHead?.header("l")?.toIntOrNull()
                ?: 0
            val totalEndChar = headEnd + 4
            val headerBytesCount = text.substring(0, totalEndChar).toByteArray(Charsets.UTF_8).size
            val frameTotalBytes = headerBytesCount + contentLen
            if (data.size < frameTotalBytes) return

            val frame = String(
                data,
                text.substring(0, trimmedStart).toByteArray(Charsets.UTF_8).size,
                frameTotalBytes - text.substring(0, trimmedStart).toByteArray(Charsets.UTF_8).size,
                Charsets.UTF_8,
            )
            val remaining = data.size - frameTotalBytes
            buffer.reset()
            if (remaining > 0) {
                buffer.write(data, frameTotalBytes, remaining)
            }
            SipMessage.parse(frame)?.let { handleSipMessage(it) }
        }
    }

    private fun handleSipMessage(msg: SipMessage) {
        if (msg.isResponse) {
            handleSipResponse(msg)
        } else {
            handleSipRequest(msg)
        }
    }

    private fun handleSipResponse(msg: SipMessage) {
        val callId = msg.callId()
        val (_, method) = msg.cseq()
        if (callId == registerCallId && method.equals("REGISTER", ignoreCase = true)) {
            handleRegisterResponse(msg)
            return
        }

        val appCallId = appIdBySipId[callId] ?: return
        val dialog = dialogsByAppId[appCallId] ?: return

        if (method.equals("INVITE", ignoreCase = true)) {
            when (msg.statusCode) {
                100 -> Unit
                180, 183 -> {
                    SdpCodec.parseRemoteAudio(msg.body)?.let { dialog.remoteRtp = it }
                    _events.tryEmit(CallProviderEvent.OutgoingRinging(appCallId))
                }
                200 -> {
                    val toTag = SipMessageBuilder.extractTag(msg.toHeader())
                    if (toTag.isNotEmpty()) dialog.remoteTag = toTag
                    msg.header("Contact")?.let { contact ->
                        val uri = SipMessageBuilder.extractUri(contact)
                        if (uri.isNotBlank()) dialog.targetUri = uri
                    }
                    sendAckForDialog(dialog)
                    dialog.established = true

                    val remoteRtp = SdpCodec.parseRemoteAudio(msg.body) ?: dialog.remoteRtp
                    if (remoteRtp != null) {
                        dialog.remoteRtp = remoteRtp
                        val pt = selectPreferredCodec(remoteRtp.payloadTypes)
                        rtpEngine?.start(remoteRtp.ip, remoteRtp.port, pt)
                    }
                    _events.tryEmit(CallProviderEvent.CallConnected(appCallId))
                }
                401, 407 -> {
                    if (!dialog.authRetried) {
                        val cfg = activeConfig
                        val challengeHeader = if (msg.statusCode == 407) {
                            msg.header("Proxy-Authenticate")
                        } else {
                            msg.header("WWW-Authenticate")
                        }
                        val challenge = DigestChallenge.parse(challengeHeader)
                        if (cfg != null && challenge != null && cfg.secret.isNotEmpty()) {
                            dialog.authRetried = true
                            sendAckForDialog(dialog, cseqOverride = dialog.inviteCseq)
                            dialog.cseq = cseqCounter.getAndIncrement()
                            dialog.inviteCseq = dialog.cseq
                            dialog.inviteBranch = "z9hG4bK${randomToken(12)}"
                            val authHeaderName = if (msg.statusCode == 407) "Proxy-Authorization" else "Authorization"
                            val authValue = challenge.buildAuthorization(
                                method = "INVITE",
                                uri = dialog.targetUri,
                                username = cfg.username,
                                password = cfg.secret,
                                cnonce = randomToken(8),
                            )
                            val rtpPort = rtpEngine?.prepareLocalSocket() ?: 40000
                            val sdp = SdpCodec.buildAudioOffer(localIp, rtpPort)
                            sendInvite(cfg, dialog, sdp, authHeaderName, authValue)
                            return
                        }
                    }
                    endDialogOnFailure(appCallId, CallEndReason.CONFIG_ERROR, "SIP kimlik doğrulama reddedildi")
                }
                486, 600, 603 -> {
                    sendAckForDialog(dialog, cseqOverride = dialog.inviteCseq)
                    endDialogOnFailure(appCallId, CallEndReason.BUSY, "Karşı taraf meşgul")
                }
                487 -> {
                    sendAckForDialog(dialog, cseqOverride = dialog.inviteCseq)
                    endDialogOnFailure(appCallId, CallEndReason.CANCELLED, "")
                }
                else -> if (msg.statusCode >= 400) {
                    sendAckForDialog(dialog, cseqOverride = dialog.inviteCseq)
                    endDialogOnFailure(
                        appCallId,
                        CallEndReason.NETWORK_ERROR,
                        "SIP hatası (${msg.statusCode})",
                    )
                }
            }
        }
    }

    private fun handleRegisterResponse(msg: SipMessage) {
        when (msg.statusCode) {
            200 -> {
                registerAuthRetried = false
                _status.value = ProviderStatusSnapshot(
                    state = ProviderRegistrationState.REGISTERED,
                    statusText = "LocalTel hazır",
                )
            }
            401, 407 -> {
                val cfg = activeConfig ?: return
                if (registerAuthRetried || cfg.secret.isEmpty()) {
                    _status.value = ProviderStatusSnapshot(
                        state = ProviderRegistrationState.ERROR,
                        statusText = "SIP parola doğrulaması başarısız",
                        lastError = "401/407 reddedildi",
                    )
                    return
                }
                val hdr = if (msg.statusCode == 407) msg.header("Proxy-Authenticate") else msg.header("WWW-Authenticate")
                val challenge = DigestChallenge.parse(hdr)
                if (challenge == null) {
                    _status.value = ProviderStatusSnapshot(
                        state = ProviderRegistrationState.ERROR,
                        statusText = "Desteklenmeyen SIP kimlik doğrulama",
                        lastError = "Digest challenge okunamadı",
                    )
                    return
                }
                registerAuthRetried = true
                val authHdrName = if (msg.statusCode == 407) "Proxy-Authorization" else "Authorization"
                val registrarUri = "sip:${cfg.effectiveDomain()}"
                val authVal = challenge.buildAuthorization(
                    method = "REGISTER",
                    uri = registrarUri,
                    username = cfg.username,
                    password = cfg.secret,
                    cnonce = randomToken(8),
                )
                sendRegister(cfg, authHdrName, authVal)
            }
            else -> if (msg.statusCode >= 400) {
                _status.value = ProviderStatusSnapshot(
                    state = ProviderRegistrationState.ERROR,
                    statusText = "SIP kayıt hatası (${msg.statusCode})",
                    lastError = msg.reasonPhrase,
                )
            }
        }
    }

    private fun handleSipRequest(msg: SipMessage) {
        val cfg = activeConfig ?: return
        when (msg.method) {
            "INVITE" -> {
                val sipCallId = msg.callId()
                // Ignore re-INVITE if dialog already exists, send 200 OK.
                val existingAppId = appIdBySipId[sipCallId]
                if (existingAppId != null) {
                    val dialog = dialogsByAppId[existingAppId]
                    if (dialog != null && dialog.established) {
                        val rtpPort = rtpEngine?.prepareLocalSocket() ?: 40000
                        val sdp = SdpCodec.buildAudioOffer(localIp, rtpPort)
                        sendRaw(SipMessageBuilder.buildResponse(msg, 200, "OK", dialog.localTag, body = sdp, contentType = "application/sdp"))
                    }
                    return
                }

                // Busy if another call is currently active.
                if (dialogsByAppId.isNotEmpty()) {
                    sendRaw(SipMessageBuilder.buildResponse(msg, 486, "Busy Here", randomToken(8)))
                    return
                }

                val appCallId = "sip_${UUID.randomUUID().toString().take(12)}"
                val localTag = randomToken(10)
                val fromHeader = msg.fromHeader()
                val remoteUri = SipMessageBuilder.extractUri(fromHeader)
                val remoteTag = SipMessageBuilder.extractTag(fromHeader)
                val contactUri = msg.header("Contact")?.let { SipMessageBuilder.extractUri(it) }?.ifBlank { remoteUri } ?: remoteUri
                val parsedCaller = PhoneNumberNormalizer.normalize(fromHeader, cfg.defaultCountryCode)

                val dialog = ActiveDialog(
                    appCallId = appCallId,
                    sipCallId = sipCallId,
                    isOutgoing = false,
                    localTag = localTag,
                    remoteTag = remoteTag,
                    remoteUri = remoteUri,
                    targetUri = contactUri,
                    cseq = msg.cseq().first,
                    inviteCseq = msg.cseq().first,
                    inviteBranch = "",
                    inboundInvite = msg,
                    remoteRtp = SdpCodec.parseRemoteAudio(msg.body),
                )
                dialogsByAppId[appCallId] = dialog
                appIdBySipId[sipCallId] = appCallId

                sendRaw(SipMessageBuilder.buildResponse(msg, 100, "Trying", localTag))
                val localContact = "sip:${cfg.username}@$localIp:$localSipPort;transport=${transportParam(cfg.transport)}"
                sendRaw(SipMessageBuilder.buildResponse(msg, 180, "Ringing", localTag, contactUri = localContact))

                _events.tryEmit(
                    CallProviderEvent.IncomingCall(
                        callId = appCallId,
                        remoteUri = remoteUri,
                        displayNameHint = parsedCaller.displayNameHint.orEmpty(),
                    ),
                )
            }

            "CANCEL" -> {
                val appCallId = appIdBySipId.remove(msg.callId()) ?: return
                val dialog = dialogsByAppId.remove(appCallId) ?: return
                sendRaw(SipMessageBuilder.buildResponse(msg, 200, "OK", dialog.localTag))
                dialog.inboundInvite?.let { inv ->
                    sendRaw(SipMessageBuilder.buildResponse(inv, 487, "Request Terminated", dialog.localTag))
                }
                rtpEngine?.stop()
                rtpEngine = null
                _events.tryEmit(CallProviderEvent.CallEnded(appCallId, CallEndReason.CANCELLED))
            }

            "BYE" -> {
                val appCallId = appIdBySipId.remove(msg.callId())
                val dialog = appCallId?.let { dialogsByAppId.remove(it) }
                sendRaw(SipMessageBuilder.buildResponse(msg, 200, "OK", dialog?.localTag ?: randomToken(6)))
                rtpEngine?.stop()
                rtpEngine = null
                if (appCallId != null) {
                    _events.tryEmit(CallProviderEvent.CallEnded(appCallId, CallEndReason.REMOTE_HANGUP))
                }
            }

            "ACK" -> Unit

            "OPTIONS" -> {
                sendRaw(SipMessageBuilder.buildResponse(msg, 200, "OK", randomToken(8)))
            }
        }
    }

    private fun sendRegister(
        cfg: LocalTelConfig,
        authHeaderName: String?,
        authHeaderValue: String?,
        expiresSec: Int = 300,
    ) {
        val domain = cfg.effectiveDomain()
        val registrarUri = "sip:$domain"
        val userUri = "sip:${cfg.username}@$domain"
        val contactUri = "sip:${cfg.username}@$localIp:$localSipPort;transport=${transportParam(cfg.transport)}"
        lastRegisterCSeq = cseqCounter.getAndIncrement()
        val req = SipMessageBuilder.buildRequest(
            method = "REGISTER",
            requestUri = registrarUri,
            viaTransport = transportLabel(cfg.transport),
            localHost = localIp,
            localPort = localSipPort,
            branch = "z9hG4bK${randomToken(12)}",
            fromDisplayName = cfg.displayName,
            fromUri = userUri,
            fromTag = registerFromTag,
            toUri = userUri,
            toTag = null,
            callId = registerCallId,
            cseq = lastRegisterCSeq,
            contactUri = contactUri,
            expiresSec = expiresSec,
            authHeaderName = authHeaderName,
            authHeaderValue = authHeaderValue,
        )
        sendRaw(req)
    }

    private fun sendInvite(
        cfg: LocalTelConfig,
        dialog: ActiveDialog,
        sdp: String,
        authHeaderName: String?,
        authHeaderValue: String?,
    ) {
        val domain = cfg.effectiveDomain()
        val userUri = "sip:${cfg.username}@$domain"
        val contactUri = "sip:${cfg.username}@$localIp:$localSipPort;transport=${transportParam(cfg.transport)}"
        val req = SipMessageBuilder.buildRequest(
            method = "INVITE",
            requestUri = dialog.targetUri,
            viaTransport = transportLabel(cfg.transport),
            localHost = localIp,
            localPort = localSipPort,
            branch = dialog.inviteBranch,
            fromDisplayName = cfg.displayName,
            fromUri = userUri,
            fromTag = dialog.localTag,
            toUri = dialog.remoteUri,
            toTag = null,
            callId = dialog.sipCallId,
            cseq = dialog.inviteCseq,
            contactUri = contactUri,
            authHeaderName = authHeaderName,
            authHeaderValue = authHeaderValue,
            contentType = "application/sdp",
            body = sdp,
        )
        sendRaw(req)
    }

    private fun sendAckForDialog(dialog: ActiveDialog, cseqOverride: Int? = null) {
        val cfg = activeConfig ?: return
        val domain = cfg.effectiveDomain()
        val userUri = "sip:${cfg.username}@$domain"
        val ack = SipMessageBuilder.buildRequest(
            method = "ACK",
            requestUri = dialog.targetUri,
            viaTransport = transportLabel(cfg.transport),
            localHost = localIp,
            localPort = localSipPort,
            branch = "z9hG4bK${randomToken(12)}",
            fromDisplayName = cfg.displayName,
            fromUri = userUri,
            fromTag = dialog.localTag,
            toUri = dialog.remoteUri,
            toTag = dialog.remoteTag.ifBlank { null },
            callId = dialog.sipCallId,
            cseq = cseqOverride ?: dialog.inviteCseq,
        )
        sendRaw(ack)
    }

    private fun endDialogOnFailure(appCallId: String, reason: CallEndReason, message: String) {
        dialogsByAppId.remove(appCallId)?.let {
            appIdBySipId.remove(it.sipCallId)
        }
        rtpEngine?.stop()
        rtpEngine = null
        _events.tryEmit(CallProviderEvent.CallError(appCallId, reason, message))
    }

    private fun sendRaw(packetText: String) {
        val bytes = packetText.toByteArray(Charsets.UTF_8)
        val udp = udpSocket
        val addr = serverInet
        if (udp != null && !udp.isClosed && addr != null) {
            udp.send(DatagramPacket(bytes, bytes.size, addr, serverPort))
            return
        }
        val out = tcpOut
        if (out != null) {
            synchronized(out) {
                out.write(bytes)
                out.flush()
            }
        }
    }

    private fun stopSocketsOnly() {
        runCatching { udpSocket?.close() }
        udpSocket = null
        runCatching { tcpOut?.close() }
        tcpOut = null
        runCatching { tcpSocket?.close() }
        tcpSocket = null
        serverInet = null
    }

    private fun formatDialTarget(rawTarget: String, prefix: String): String {
        val trimmed = rawTarget.trim()
        if (prefix.isBlank()) return trimmed
        if (trimmed.contains('@') || trimmed.startsWith("sip:", true)) return trimmed
        return prefix + trimmed
    }

    private fun selectPreferredCodec(payloadTypes: List<Int>): Int =
        when {
            payloadTypes.contains(G711Codec.PAYLOAD_TYPE_PCMU) -> G711Codec.PAYLOAD_TYPE_PCMU
            payloadTypes.contains(G711Codec.PAYLOAD_TYPE_PCMA) -> G711Codec.PAYLOAD_TYPE_PCMA
            else -> G711Codec.PAYLOAD_TYPE_PCMU
        }

    private fun transportLabel(transport: LocalTelTransport): String =
        when (transport) {
            LocalTelTransport.TLS -> "TLS"
            LocalTelTransport.TCP -> "TCP"
            else -> "UDP"
        }

    private fun transportParam(transport: LocalTelTransport): String =
        when (transport) {
            LocalTelTransport.TLS -> "tls"
            LocalTelTransport.TCP -> "tcp"
            else -> "udp"
        }

    private fun randomToken(length: Int): String {
        val chars = "abcdefghijklmnopqrstuvwxyz0123456789"
        return buildString(length) {
            repeat(length) {
                append(chars[random.nextInt(chars.length)])
            }
        }
    }

    companion object {
        private const val TAG = "SipLocalTelProvider"
    }
}
