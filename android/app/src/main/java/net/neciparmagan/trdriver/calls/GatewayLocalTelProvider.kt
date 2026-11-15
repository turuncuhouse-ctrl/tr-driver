package net.neciparmagan.trdriver.calls

import android.annotation.SuppressLint
import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.AudioTrack
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.NoiseSuppressor
import android.util.Base64
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
import kotlinx.serialization.json.Json
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.io.IOException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import javax.net.ssl.SSLException
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * WebSocket / HTTP gateway implementation of [LocalTelCallProvider].
 * Connects either to a custom LocalTel gateway URL or to the authenticated
 * TR Driver relay endpoint `/api/localtel/ws`, carrying JSON call signals and
 * G.711 PCMU voice frames for NAT-friendly calling across Wi-Fi & mobile data.
 */
class GatewayLocalTelProvider(
    private val context: Context,
    private val session: SessionStore,
) : LocalTelCallProvider {
    override val providerId: String = "gateway"

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val json = Json {
        encodeDefaults = false
        ignoreUnknownKeys = true
        isLenient = true
    }

    private val http = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.SECONDS)
        .pingInterval(25, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    private val _status = MutableStateFlow(ProviderStatusSnapshot())
    override val statusFlow: StateFlow<ProviderStatusSnapshot> = _status.asStateFlow()

    private val _events = MutableSharedFlow<CallProviderEvent>(extraBufferCapacity = 32)
    override val events: SharedFlow<CallProviderEvent> = _events.asSharedFlow()

    private var activeConfig: LocalTelConfig? = null
    private var webSocket: WebSocket? = null
    private var pingJob: Job? = null
    private var activeCallId: String? = null
    private val audioActive = AtomicBoolean(false)
    private val muted = AtomicBoolean(false)
    private var audioRecord: AudioRecord? = null
    private var audioTrack: AudioTrack? = null
    private var aec: AcousticEchoCanceler? = null
    private var ns: NoiseSuppressor? = null
    private var txThread: Thread? = null

    override suspend fun start(config: LocalTelConfig): Result<Unit> = withContext(Dispatchers.IO) {
        stop()
        val wsUrl = resolveWebSocketUrl(config)
        if (wsUrl.isBlank()) {
            _status.value = ProviderStatusSnapshot(
                state = ProviderRegistrationState.ERROR,
                statusText = "Gateway adresi tanımlı değil",
                lastError = "Geçerli sunucu adresi gerekli",
            )
            return@withContext Result.failure(IllegalStateException("Gateway URL eksik"))
        }

        activeConfig = config
        _status.value = ProviderStatusSnapshot(
            state = ProviderRegistrationState.REGISTERING,
            statusText = "LocalTel ağ geçidine bağlanıyor...",
        )

        val reqBuilder = Request.Builder().url(wsUrl)
        val usingTrDriverRelay = config.serverUrl.isBlank() ||
            wsUrl.contains("/localtel/ws", ignoreCase = true)
        if (usingTrDriverRelay) {
            val token = config.gatewayToken.ifBlank { session.token.orEmpty() }
            if (token.isBlank()) {
                _status.value = ProviderStatusSnapshot(
                    state = ProviderRegistrationState.ERROR,
                    statusText = "TR Driver oturumu açık değil",
                    lastError = "Oturum gerekli",
                )
                return@withContext Result.failure(IllegalStateException("TR Driver oturumu gerekli"))
            }
            reqBuilder.header("Authorization", "Bearer $token")
        } else if (config.secret.isNotBlank()) {
            reqBuilder.header("Authorization", "Bearer ${config.secret}")
        }

        val ws = http.newWebSocket(reqBuilder.build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                _status.value = ProviderStatusSnapshot(
                    state = ProviderRegistrationState.REGISTERED,
                    statusText = "LocalTel ağ geçidi hazır",
                )
                startPingLoop()
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                handleIncomingMessage(text)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                if (this@GatewayLocalTelProvider.webSocket === webSocket) {
                    this@GatewayLocalTelProvider.webSocket = null
                    pingJob?.cancel()
                    pingJob = null
                    // A close we did not ask for must trigger the listener's
                    // reconnect loop (ERROR), not stay silently DISABLED.
                    _status.value = ProviderStatusSnapshot(
                        state = ProviderRegistrationState.ERROR,
                        statusText = "Bağlantı kapandı; yeniden bağlanılıyor",
                        lastError = "Ağ geçidi bağlantısı kapandı",
                    )
                }
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                Log.w(TAG, "Gateway WebSocket connection failed")
                if (this@GatewayLocalTelProvider.webSocket === webSocket) {
                    this@GatewayLocalTelProvider.webSocket = null
                    val turkishReason = when (t) {
                        is UnknownHostException -> "Sunucu adresi bulunamadı; internet bağlantınızı kontrol edin."
                        is SocketTimeoutException -> "Sunucu yanıt vermedi; bağlantı yeniden denenecek."
                        is SSLException -> "Güvenli bağlantı kurulamadı; sunucu sertifikasını kontrol edin."
                        is IOException -> "Ağ bağlantısı kesildi; otomatik yeniden denenecek."
                        else -> "Ağ geçidine bağlanılamadı; otomatik yeniden denenecek."
                    }
                    _status.value = ProviderStatusSnapshot(
                        state = ProviderRegistrationState.ERROR,
                        statusText = "Ağ geçidi bağlantı hatası",
                        lastError = turkishReason,
                    )
                    activeCallId?.let { cid ->
                        stopAudioBridge()
                        activeCallId = null
                        _events.tryEmit(
                            CallProviderEvent.CallError(
                                cid,
                                CallEndReason.NETWORK_ERROR,
                                "Ağ geçidi bağlantısı kesildi",
                            ),
                        )
                    }
                }
            }
        })
        webSocket = ws
        Result.success(Unit)
    }

    override suspend fun stop() = withContext(Dispatchers.IO) {
        pingJob?.cancel()
        pingJob = null
        stopAudioBridge()
        activeCallId = null
        runCatching { webSocket?.close(1000, "stopped") }
        webSocket = null
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
            val ws = webSocket ?: error("Gateway WebSocket bağlantısı açık değil")
            activeCallId = callId
            val target = if (config.outboundPrefix.isNotBlank() && !targetNumber.contains('@')) {
                config.outboundPrefix + targetNumber
            } else {
                targetNumber
            }
            sendSignal(
                ws,
                LocalTelSignalDto(
                    type = "invite",
                    callId = callId,
                    number = target,
                    displayName = config.displayName,
                    timestampMs = System.currentTimeMillis(),
                ),
            )
            Unit
        }
    }

    override suspend fun acceptIncomingCall(callId: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            val ws = webSocket ?: error("Gateway bağlantısı kapalı")
            activeCallId = callId
            sendSignal(
                ws,
                LocalTelSignalDto(
                    type = "accept",
                    callId = callId,
                    timestampMs = System.currentTimeMillis(),
                ),
            )
            startAudioBridge(callId)
            _events.tryEmit(CallProviderEvent.CallConnected(callId))
            Unit
        }
    }

    override suspend fun rejectIncomingCall(callId: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            webSocket?.let { ws ->
                sendSignal(
                    ws,
                    LocalTelSignalDto(
                        type = "reject",
                        callId = callId,
                        timestampMs = System.currentTimeMillis(),
                    ),
                )
            }
            if (activeCallId == callId) {
                activeCallId = null
                stopAudioBridge()
            }
        }
    }

    override suspend fun hangupCall(callId: String): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            webSocket?.let { ws ->
                sendSignal(
                    ws,
                    LocalTelSignalDto(
                        type = "hangup",
                        callId = callId,
                        timestampMs = System.currentTimeMillis(),
                    ),
                )
            }
            if (activeCallId == callId) {
                activeCallId = null
                stopAudioBridge()
            }
        }
    }

    override suspend fun setMuted(callId: String, muted: Boolean) {
        this.muted.set(muted)
    }

    override suspend fun sendDtmf(callId: String, digit: Char) {
        val ws = webSocket ?: return
        sendSignal(
            ws,
            LocalTelSignalDto(
                type = "audio",
                callId = callId,
                payload = "dtmf:$digit",
                timestampMs = System.currentTimeMillis(),
            ),
        )
    }

    private fun startPingLoop() {
        pingJob?.cancel()
        pingJob = scope.launch {
            while (isActive) {
                delay(25_000L)
                val ws = webSocket ?: break
                sendSignal(ws, LocalTelSignalDto(type = "ping", timestampMs = System.currentTimeMillis()))
            }
        }
    }

    private fun handleIncomingMessage(text: String) {
        val msg = runCatching { json.decodeFromString(LocalTelSignalDto.serializer(), text) }.getOrNull() ?: return
        when (msg.type.lowercase()) {
            "invite" -> {
                if (msg.callId.isBlank()) return
                if (activeCallId != null && activeCallId != msg.callId) {
                    webSocket?.let { ws ->
                        sendSignal(ws, LocalTelSignalDto(type = "reject", callId = msg.callId))
                    }
                    return
                }
                activeCallId = msg.callId
                webSocket?.let { ws ->
                    sendSignal(ws, LocalTelSignalDto(type = "ringing", callId = msg.callId))
                }
                _events.tryEmit(
                    CallProviderEvent.IncomingCall(
                        callId = msg.callId,
                        remoteUri = msg.number,
                        displayNameHint = msg.displayName,
                    ),
                )
            }
            "ringing" -> {
                if (msg.callId == activeCallId) {
                    _events.tryEmit(CallProviderEvent.OutgoingRinging(msg.callId))
                }
            }
            "accept" -> {
                if (msg.callId == activeCallId) {
                    startAudioBridge(msg.callId)
                    _events.tryEmit(CallProviderEvent.CallConnected(msg.callId))
                }
            }
            "reject" -> {
                if (msg.callId == activeCallId) {
                    stopAudioBridge()
                    activeCallId = null
                    // The server states why an invite could not be delivered;
                    // an empty reason means the callee pressed "Reddet".
                    when (msg.payload) {
                        "unknown_target", "caller_unprovisioned" -> _events.tryEmit(
                            CallProviderEvent.CallError(
                                msg.callId, CallEndReason.NOT_FOUND,
                                "Bulunamadı: bu kişi TR Driver kullanmıyor ya da numarasını kaydetmemiş. " +
                                    "Aramalar ekranındaki \"TR Driver kullanıcıları\" listesinden arayın.",
                            ),
                        )
                        "callee_offline" -> _events.tryEmit(
                            CallProviderEvent.CallError(
                                msg.callId, CallEndReason.OFFLINE,
                                "Çevrimdışı: karşı tarafın uygulaması şu an bağlı değil",
                            ),
                        )
                        "busy" -> _events.tryEmit(
                            CallProviderEvent.CallError(
                                msg.callId, CallEndReason.BUSY,
                                "Meşgul: karşı taraf başka bir görüşmede",
                            ),
                        )
                        "self" -> _events.tryEmit(
                            CallProviderEvent.CallError(
                                msg.callId, CallEndReason.CONFIG_ERROR,
                                "Kendi numaranızı arayamazsınız",
                            ),
                        )
                        "rate_limited", "call_conflict" -> _events.tryEmit(
                            CallProviderEvent.CallError(
                                msg.callId, CallEndReason.NETWORK_ERROR,
                                "Arama şu an başlatılamadı, biraz sonra tekrar deneyin",
                            ),
                        )
                        else -> _events.tryEmit(CallProviderEvent.CallEnded(msg.callId, CallEndReason.REJECTED))
                    }
                }
            }
            "cancel", "hangup" -> {
                if (msg.callId == activeCallId) {
                    stopAudioBridge()
                    activeCallId = null
                    val reason = if (msg.type == "cancel") CallEndReason.CANCELLED else CallEndReason.REMOTE_HANGUP
                    _events.tryEmit(CallProviderEvent.CallEnded(msg.callId, reason))
                }
            }
            "audio" -> {
                if (msg.callId == activeCallId && !msg.payload.startsWith("dtmf:") && msg.payload.isNotBlank()) {
                    val raw = runCatching { Base64.decode(msg.payload, Base64.NO_WRAP) }.getOrNull() ?: return
                    val pcm = G711Codec.decodeFrame(raw, 0, raw.size, G711Codec.PAYLOAD_TYPE_PCMU)
                    audioTrack?.write(pcm, 0, pcm.size)
                }
            }
        }
    }

    @SuppressLint("MissingPermission")
    private fun startAudioBridge(callId: String) {
        if (!CallPermissions.hasMicrophone(context)) {
            audioActive.set(false)
            _status.value = ProviderStatusSnapshot(
                state = ProviderRegistrationState.ERROR,
                statusText = "Hata: mikrofon izni verilmedi",
                lastError = "Arama sesi için mikrofon izni gerekli",
            )
            return
        }
        if (audioActive.getAndSet(true)) return
        val minOut = AudioTrack.getMinBufferSize(
            G711Codec.SAMPLE_RATE_HZ,
            AudioFormat.CHANNEL_OUT_MONO,
            AudioFormat.ENCODING_PCM_16BIT,
        ).coerceAtLeast(G711Codec.FRAME_SAMPLES * 8)
        runCatching {
            val track = AudioTrack.Builder()
                .setAudioAttributes(
                    AudioAttributes.Builder()
                        .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                        .build(),
                )
                .setAudioFormat(
                    AudioFormat.Builder()
                        .setSampleRate(G711Codec.SAMPLE_RATE_HZ)
                        .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                        .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                        .build(),
                )
                .setBufferSizeInBytes(minOut)
                .setTransferMode(AudioTrack.MODE_STREAM)
                .build()
            track.play()
            audioTrack = track
        }

        if (!CallPermissions.hasMicrophone(context)) {
            audioActive.set(false)
            return
        }
        val minIn = AudioRecord.getMinBufferSize(
            G711Codec.SAMPLE_RATE_HZ,
            AudioFormat.CHANNEL_IN_MONO,
            AudioFormat.ENCODING_PCM_16BIT,
        ).coerceAtLeast(G711Codec.FRAME_SAMPLES * 8)

        runCatching {
            val rec = AudioRecord(
                MediaRecorder.AudioSource.VOICE_COMMUNICATION,
                G711Codec.SAMPLE_RATE_HZ,
                AudioFormat.CHANNEL_IN_MONO,
                AudioFormat.ENCODING_PCM_16BIT,
                minIn,
            )
            if (rec.state != AudioRecord.STATE_INITIALIZED) {
                rec.release()
                return
            }
            if (AcousticEchoCanceler.isAvailable()) {
                aec = runCatching { AcousticEchoCanceler.create(rec.audioSessionId)?.apply { enabled = true } }.getOrNull()
            }
            if (NoiseSuppressor.isAvailable()) {
                ns = runCatching { NoiseSuppressor.create(rec.audioSessionId)?.apply { enabled = true } }.getOrNull()
            }
            audioRecord = rec
            rec.startRecording()

            txThread = Thread({
                val pcm = ShortArray(G711Codec.FRAME_SAMPLES * 2) // 40ms frames
                while (audioActive.get()) {
                    val read = rec.read(pcm, 0, pcm.size)
                    if (read <= 0) continue
                    if (muted.get()) pcm.fill(0, 0, read)
                    val encoded = G711Codec.encodeFrame(pcm, read, G711Codec.PAYLOAD_TYPE_PCMU)
                    val ws = webSocket ?: continue
                    sendSignal(
                        ws,
                        LocalTelSignalDto(
                            type = "audio",
                            callId = callId,
                            payload = Base64.encodeToString(encoded, Base64.NO_WRAP),
                        ),
                    )
                }
            }, "TR-GW-TX").apply { start() }
        }
    }

    private fun stopAudioBridge() {
        audioActive.set(false)
        runCatching { txThread?.join(200) }
        txThread = null
        runCatching { aec?.release() }
        runCatching { ns?.release() }
        aec = null
        ns = null
        runCatching {
            audioRecord?.let {
                if (it.recordingState == AudioRecord.RECORDSTATE_RECORDING) it.stop()
                it.release()
            }
        }
        audioRecord = null
        runCatching {
            audioTrack?.let {
                if (it.playState == AudioTrack.PLAYSTATE_PLAYING) it.stop()
                it.release()
            }
        }
        audioTrack = null
    }

    private fun sendSignal(ws: WebSocket, dto: LocalTelSignalDto) {
        runCatching {
            ws.send(json.encodeToString(LocalTelSignalDto.serializer(), dto))
        }
    }

    private fun resolveWebSocketUrl(config: LocalTelConfig): String {
        val raw = config.serverUrl.trim()
        if (raw.isEmpty()) {
            val base = session.serverUrl.trim().trimEnd('/')
            if (base.isEmpty()) return ""
            val wsBase = when {
                base.startsWith("https://", true) -> "wss://" + base.substring(8)
                base.startsWith("http://", true) -> "ws://" + base.substring(7)
                else -> "wss://$base"
            }
            return "$wsBase/localtel/ws"
        }
        return when {
            raw.startsWith("wss://", true) || raw.startsWith("ws://", true) -> raw
            raw.startsWith("https://", true) -> "wss://" + raw.substring(8)
            raw.startsWith("http://", true) -> "ws://" + raw.substring(7)
            else -> "wss://$raw"
        }
    }

    companion object {
        private const val TAG = "GatewayLocalTelProvider"
    }
}
