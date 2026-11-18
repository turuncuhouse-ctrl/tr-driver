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
import android.util.Log
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.security.SecureRandom
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

/**
 * Bidirectional G.711 PCMU/PCMA over UDP RTP audio engine.
 * Supports RFC 2833 DTMF tones and symmetric NAT RTP learning.
 */
class RtpAudioEngine(context: Context) {
    private val appContext = context.applicationContext
    private val running = AtomicBoolean(false)
    private val muted = AtomicBoolean(false)
    private val dtmfSeq = AtomicInteger(0)

    private var socket: DatagramSocket? = null
    private var txThread: Thread? = null
    private var rxThread: Thread? = null

    @Volatile
    private var remoteAddr: InetAddress? = null

    @Volatile
    private var remotePort: Int = 0

    @Volatile
    private var payloadType: Int = G711Codec.PAYLOAD_TYPE_PCMU

    private var rtpSeq = SecureRandom().nextInt(0xffff)
    private var rtpTs = SecureRandom().nextInt() and 0x7fffffff
    private val rtpSsrc = (SecureRandom().nextInt().toLong() and 0xffffffffL)

    @Volatile
    private var audioRecord: AudioRecord? = null

    @Volatile
    private var audioTrack: AudioTrack? = null

    @Volatile
    private var aec: AcousticEchoCanceler? = null

    @Volatile
    private var ns: NoiseSuppressor? = null

    fun prepareLocalSocket(): Int {
        if (socket != null && !socket!!.isClosed) {
            return socket!!.localPort
        }
        val sock = DatagramSocket(0)
        CallNetwork.bind(sock)
        sock.soTimeout = 500
        socket = sock
        return sock.localPort
    }

    fun start(
        remoteHost: String,
        remoteRtpPort: Int,
        negotiatedPayloadType: Int = G711Codec.PAYLOAD_TYPE_PCMU,
    ): Boolean {
        if (running.getAndSet(true)) return true
        return try {
            val sock = socket ?: DatagramSocket(0).also {
                CallNetwork.bind(it)
                it.soTimeout = 500
                socket = it
            }
            remoteAddr = InetAddress.getByName(remoteHost)
            remotePort = remoteRtpPort
            payloadType = if (negotiatedPayloadType == G711Codec.PAYLOAD_TYPE_PCMA) {
                G711Codec.PAYLOAD_TYPE_PCMA
            } else {
                G711Codec.PAYLOAD_TYPE_PCMU
            }

            startAudioTrack()
            startRxLoop(sock)
            if (CallPermissions.hasMicrophone(appContext)) {
                startTxLoop(sock)
            }
            true
        } catch (_: Throwable) {
            Log.w(TAG, "RTP engine start failed")
            stop()
            false
        }
    }

    fun setMuted(isMuted: Boolean) {
        muted.set(isMuted)
    }

    fun sendDtmf(digit: Char) {
        if (!running.get()) return
        val eventCode = when (digit) {
            in '0'..'9' -> digit - '0'
            '*' -> 10
            '#' -> 11
            else -> return
        }
        val sock = socket ?: return
        val addr = remoteAddr ?: return
        val port = remotePort
        if (port !in 1..65535) return

        Thread {
            runCatching {
                val ts = rtpTs.toLong()
                repeat(3) { idx ->
                    val endFlag = if (idx == 2) 0x80 else 0x00
                    val duration = (idx + 1) * G711Codec.FRAME_SAMPLES
                    val body = byteArrayOf(
                        eventCode.toByte(),
                        (endFlag or 10).toByte(),
                        ((duration ushr 8) and 0xff).toByte(),
                        (duration and 0xff).toByte(),
                    )
                    val pkt = RtpPacket(101, rtpSeq++, ts, rtpSsrc, body).encode()
                    sock.send(DatagramPacket(pkt, pkt.size, addr, port))
                    Thread.sleep(20)
                }
            }
        }.start()
    }

    fun stop() {
        running.set(false)
        runCatching { socket?.close() }
        socket = null
        runCatching { txThread?.join(250) }
        runCatching { rxThread?.join(250) }
        txThread = null
        rxThread = null

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
        dtmfSeq.set(0)
    }

    private fun startAudioTrack() {
        val minBuf = AudioTrack.getMinBufferSize(
            G711Codec.SAMPLE_RATE_HZ,
            AudioFormat.CHANNEL_OUT_MONO,
            AudioFormat.ENCODING_PCM_16BIT,
        ).coerceAtLeast(G711Codec.FRAME_SAMPLES * 8)

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
            .setBufferSizeInBytes(minBuf)
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()
        track.play()
        audioTrack = track
    }

    @SuppressLint("MissingPermission")
    private fun startTxLoop(sock: DatagramSocket) {
        if (!CallPermissions.hasMicrophone(appContext)) return
        val minBuf = AudioRecord.getMinBufferSize(
            G711Codec.SAMPLE_RATE_HZ,
            AudioFormat.CHANNEL_IN_MONO,
            AudioFormat.ENCODING_PCM_16BIT,
        ).coerceAtLeast(G711Codec.FRAME_SAMPLES * 8)

        val rec = AudioRecord(
            MediaRecorder.AudioSource.VOICE_COMMUNICATION,
            G711Codec.SAMPLE_RATE_HZ,
            AudioFormat.CHANNEL_IN_MONO,
            AudioFormat.ENCODING_PCM_16BIT,
            minBuf,
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
            val pcm = ShortArray(G711Codec.FRAME_SAMPLES)
            while (running.get()) {
                val read = rec.read(pcm, 0, pcm.size)
                if (read <= 0) continue
                val addr = remoteAddr ?: continue
                val port = remotePort
                if (port !in 1..65535) continue
                if (muted.get()) {
                    pcm.fill(0, 0, read)
                }
                val encoded = G711Codec.encodeFrame(pcm, read, payloadType)
                val pktBytes = RtpPacket(
                    payloadType = payloadType,
                    sequence = rtpSeq and 0xffff,
                    timestamp = rtpTs.toLong() and 0xffffffffL,
                    ssrc = rtpSsrc,
                    payload = encoded,
                ).encode()
                rtpSeq = (rtpSeq + 1) and 0xffff
                rtpTs += read
                runCatching {
                    sock.send(DatagramPacket(pktBytes, pktBytes.size, addr, port))
                }
            }
        }, "TR-RTP-TX").apply { start() }
    }

    private fun startRxLoop(sock: DatagramSocket) {
        rxThread = Thread({
            val buf = ByteArray(2048)
            val dp = DatagramPacket(buf, buf.size)
            while (running.get()) {
                val ok = runCatching { sock.receive(dp) }.isSuccess
                if (!ok) continue
                if (dp.port in 1..65535 && dp.address != null) {
                    // Symmetric RTP learning for NAT traversal.
                    remoteAddr = dp.address
                    remotePort = dp.port
                }
                val pkt = RtpPacket.decode(dp.data, dp.length) ?: continue
                if (pkt.payloadType != G711Codec.PAYLOAD_TYPE_PCMU &&
                    pkt.payloadType != G711Codec.PAYLOAD_TYPE_PCMA
                ) {
                    continue
                }
                val pcm = G711Codec.decodeFrame(pkt.payload, 0, pkt.payload.size, pkt.payloadType)
                audioTrack?.write(pcm, 0, pcm.size)
            }
        }, "TR-RTP-RX").apply { start() }
    }

    companion object {
        private const val TAG = "RtpAudioEngine"
    }
}
