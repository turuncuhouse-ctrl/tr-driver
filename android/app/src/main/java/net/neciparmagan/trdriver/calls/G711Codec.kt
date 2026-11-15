package net.neciparmagan.trdriver.calls

/**
 * Pure Kotlin ITU-T G.711 u-law / A-law codec and RFC 3550 RTP packet packing.
 */
object G711Codec {
    const val PAYLOAD_TYPE_PCMU = 0
    const val PAYLOAD_TYPE_PCMA = 8
    const val SAMPLE_RATE_HZ = 8000
    const val FRAME_SAMPLES = 160 // 20ms at 8kHz

    private const val MULAW_BIAS = 0x84
    private const val MULAW_MAX = 0x7fff

    private val EXP_LUT = intArrayOf(
        0, 0, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 3, 3, 3, 3,
        4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
        5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
        5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
        6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
        6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
        6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
        6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
        7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
    )

    fun linearToMuLaw(sample: Short): Byte {
        var pcm = sample.toInt()
        val sign = (pcm shr 8) and 0x80
        if (sign != 0) pcm = -pcm
        if (pcm > MULAW_MAX) pcm = MULAW_MAX
        pcm += MULAW_BIAS
        val exponent = EXP_LUT[(pcm shr 7) and 0xff]
        val mantissa = (pcm shr (exponent + 3)) and 0x0f
        return (sign or (exponent shl 4) or mantissa).inv().toByte()
    }

    fun muLawToLinear(ulawByte: Byte): Short {
        val ulaw = ulawByte.toInt().inv() and 0xff
        val sign = ulaw and 0x80
        val exponent = (ulaw shr 4) and 0x07
        val mantissa = ulaw and 0x0f
        var sample = ((mantissa shl 3) + MULAW_BIAS) shl exponent
        sample -= MULAW_BIAS
        return (if (sign != 0) -sample else sample).toShort()
    }

    fun linearToALaw(sample: Short): Byte {
        var pcm = sample.toInt()
        val mask: Int
        if (pcm >= 0) {
            mask = 0xD5
        } else {
            mask = 0x55
            pcm = -pcm - 1
        }
        if (pcm > 32767) pcm = 32767
        val seg: Int = when {
            pcm >= 16384 -> 7
            pcm >= 8192 -> 6
            pcm >= 4096 -> 5
            pcm >= 2048 -> 4
            pcm >= 1024 -> 3
            pcm >= 512 -> 2
            pcm >= 256 -> 1
            else -> 0
        }
        val aval = if (seg < 2) {
            (seg shl 4) or ((pcm shr 4) and 0x0f)
        } else {
            (seg shl 4) or ((pcm shr (seg + 3)) and 0x0f)
        }
        return (aval xor mask).toByte()
    }

    fun aLawToLinear(alawByte: Byte): Short {
        val a = (alawByte.toInt() xor 0x55) and 0xff
        var t = (a and 0x0f) shl 4
        val seg = (a and 0x70) ushr 4
        when (seg) {
            0 -> t += 8
            1 -> t += 0x108
            else -> {
                t += 0x108
                t = t shl (seg - 1)
            }
        }
        return (if ((a and 0x80) != 0) t else -t).toShort()
    }

    fun encodeFrame(pcm: ShortArray, count: Int, payloadType: Int = PAYLOAD_TYPE_PCMU): ByteArray {
        val out = ByteArray(count)
        if (payloadType == PAYLOAD_TYPE_PCMA) {
            for (i in 0 until count) out[i] = linearToALaw(pcm[i])
        } else {
            for (i in 0 until count) out[i] = linearToMuLaw(pcm[i])
        }
        return out
    }

    fun decodeFrame(encoded: ByteArray, offset: Int, length: Int, payloadType: Int = PAYLOAD_TYPE_PCMU): ShortArray {
        val out = ShortArray(length)
        if (payloadType == PAYLOAD_TYPE_PCMA) {
            for (i in 0 until length) out[i] = aLawToLinear(encoded[offset + i])
        } else {
            for (i in 0 until length) out[i] = muLawToLinear(encoded[offset + i])
        }
        return out
    }
}

data class RtpPacket(
    val payloadType: Int,
    val sequence: Int,
    val timestamp: Long,
    val ssrc: Long,
    val payload: ByteArray,
) {
    fun encode(): ByteArray {
        val out = ByteArray(12 + payload.size)
        out[0] = 0x80.toByte() // V=2, P=0, X=0, CC=0
        out[1] = (payloadType and 0x7f).toByte()
        out[2] = ((sequence ushr 8) and 0xff).toByte()
        out[3] = (sequence and 0xff).toByte()
        out[4] = ((timestamp ushr 24) and 0xff).toByte()
        out[5] = ((timestamp ushr 16) and 0xff).toByte()
        out[6] = ((timestamp ushr 8) and 0xff).toByte()
        out[7] = (timestamp and 0xff).toByte()
        out[8] = ((ssrc ushr 24) and 0xff).toByte()
        out[9] = ((ssrc ushr 16) and 0xff).toByte()
        out[10] = ((ssrc ushr 8) and 0xff).toByte()
        out[11] = (ssrc and 0xff).toByte()
        System.arraycopy(payload, 0, out, 12, payload.size)
        return out
    }

    companion object {
        fun decode(data: ByteArray, length: Int = data.size): RtpPacket? {
            if (length < 12) return null
            val b0 = data[0].toInt() and 0xff
            val version = (b0 ushr 6) and 0x03
            if (version != 2) return null
            val hasExtension = (b0 and 0x10) != 0
            val csrcCount = b0 and 0x0f
            var headerLen = 12 + csrcCount * 4
            if (length < headerLen) return null
            if (hasExtension) {
                if (length < headerLen + 4) return null
                val extWords = ((data[headerLen + 2].toInt() and 0xff) shl 8) or
                    (data[headerLen + 3].toInt() and 0xff)
                headerLen += 4 + extWords * 4
                if (length < headerLen) return null
            }
            val payloadType = data[1].toInt() and 0x7f
            val seq = ((data[2].toInt() and 0xff) shl 8) or (data[3].toInt() and 0xff)
            val ts = ((data[4].toLong() and 0xff) shl 24) or
                ((data[5].toLong() and 0xff) shl 16) or
                ((data[6].toLong() and 0xff) shl 8) or
                (data[7].toLong() and 0xff)
            val ssrc = ((data[8].toLong() and 0xff) shl 24) or
                ((data[9].toLong() and 0xff) shl 16) or
                ((data[10].toLong() and 0xff) shl 8) or
                (data[11].toLong() and 0xff)
            val payloadLen = length - headerLen
            if (payloadLen <= 0) return null
            val payload = ByteArray(payloadLen)
            System.arraycopy(data, headerLen, payload, 0, payloadLen)
            return RtpPacket(payloadType, seq, ts, ssrc, payload)
        }
    }
}
