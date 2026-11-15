package net.neciparmagan.trdriver.calls

import java.security.MessageDigest
import java.util.Locale

data class SipHeader(val name: String, val value: String)

data class SipMessage(
    val isResponse: Boolean,
    val method: String = "",
    val requestUri: String = "",
    val statusCode: Int = 0,
    val reasonPhrase: String = "",
    val headers: List<SipHeader> = emptyList(),
    val body: String = "",
) {
    fun header(name: String): String? =
        headers.firstOrNull { it.name.equals(name, ignoreCase = true) }?.value

    fun headerValues(name: String): List<String> =
        headers.filter { it.name.equals(name, ignoreCase = true) }.map { it.value }

    fun callId(): String = header("Call-ID") ?: header("i").orEmpty()

    fun cseq(): Pair<Int, String> {
        val raw = header("CSeq").orEmpty().trim()
        val parts = raw.split(Regex("\\s+"), limit = 2)
        val num = parts.getOrNull(0)?.toIntOrNull() ?: 1
        val meth = parts.getOrNull(1).orEmpty()
        return num to meth
    }

    fun fromHeader(): String = header("From") ?: header("f").orEmpty()
    fun toHeader(): String = header("To") ?: header("t").orEmpty()
    fun viaHeaders(): List<String> = headerValues("Via").ifEmpty { headerValues("v") }

    companion object {
        fun parse(raw: String): SipMessage? {
            val normalized = raw.replace("\r\n", "\n")
            val splitIdx = normalized.indexOf("\n\n")
            val headBlock = if (splitIdx >= 0) normalized.substring(0, splitIdx) else normalized
            val body = if (splitIdx >= 0) normalized.substring(splitIdx + 2) else ""
            val lines = headBlock.split('\n').filter { it.isNotBlank() }
            if (lines.isEmpty()) return null
            val start = lines.first().trim()

            // Unfold RFC 3261 multi-line headers.
            val unfolded = ArrayList<String>()
            for (line in lines.drop(1)) {
                if ((line.startsWith(' ') || line.startsWith('\t')) && unfolded.isNotEmpty()) {
                    unfolded[unfolded.lastIndex] = unfolded.last() + " " + line.trim()
                } else {
                    unfolded.add(line)
                }
            }
            val headers = unfolded.mapNotNull { line ->
                val colon = line.indexOf(':')
                if (colon <= 0) null
                else SipHeader(line.substring(0, colon).trim(), line.substring(colon + 1).trim())
            }

            return if (start.startsWith("SIP/2.0 ", ignoreCase = true)) {
                val parts = start.split(' ', limit = 3)
                val code = parts.getOrNull(1)?.toIntOrNull() ?: return null
                val reason = parts.getOrNull(2).orEmpty()
                SipMessage(
                    isResponse = true,
                    statusCode = code,
                    reasonPhrase = reason,
                    headers = headers,
                    body = body,
                )
            } else {
                val parts = start.split(' ', limit = 3)
                if (parts.size < 3 || !parts[2].startsWith("SIP/2.0", ignoreCase = true)) {
                    return null
                }
                SipMessage(
                    isResponse = false,
                    method = parts[0].uppercase(Locale.US),
                    requestUri = parts[1],
                    headers = headers,
                    body = body,
                )
            }
        }
    }
}

data class DigestChallenge(
    val scheme: String,
    val realm: String,
    val nonce: String,
    val qop: String? = null,
    val opaque: String? = null,
    val algorithm: String = "MD5",
) {
    fun buildAuthorization(
        method: String,
        uri: String,
        username: String,
        password: String,
        cnonce: String,
        nc: String = "00000001",
    ): String {
        val hashAlg = if (algorithm.equals("SHA-256", ignoreCase = true)) "SHA-256" else "MD5"
        val ha1 = hexDigest(hashAlg, "$username:$realm:$password")
        val ha2 = hexDigest(hashAlg, "$method:$uri")
        val selectedQop = qop?.split(',')?.map { it.trim() }?.firstOrNull { it.equals("auth", ignoreCase = true) }

        val response = if (selectedQop != null) {
            hexDigest(hashAlg, "$ha1:$nonce:$nc:$cnonce:$selectedQop:$ha2")
        } else {
            hexDigest(hashAlg, "$ha1:$nonce:$ha2")
        }

        return buildString {
            append("Digest username=\"").append(username).append("\", ")
            append("realm=\"").append(realm).append("\", ")
            append("nonce=\"").append(nonce).append("\", ")
            append("uri=\"").append(uri).append("\", ")
            append("response=\"").append(response).append("\", ")
            append("algorithm=").append(algorithm)
            if (selectedQop != null) {
                append(", qop=").append(selectedQop)
                append(", nc=").append(nc)
                append(", cnonce=\"").append(cnonce).append("\"")
            }
            if (!opaque.isNullOrEmpty()) {
                append(", opaque=\"").append(opaque).append("\"")
            }
        }
    }

    companion object {
        fun parse(headerValue: String?): DigestChallenge? {
            if (headerValue.isNullOrBlank()) return null
            val trimmed = headerValue.trim()
            if (!trimmed.startsWith("Digest ", ignoreCase = true)) return null
            val params = parseAuthParams(trimmed.substring(7))
            val realm = params["realm"] ?: return null
            val nonce = params["nonce"] ?: return null
            return DigestChallenge(
                scheme = "Digest",
                realm = realm,
                nonce = nonce,
                qop = params["qop"],
                opaque = params["opaque"],
                algorithm = params["algorithm"] ?: "MD5",
            )
        }

        private fun parseAuthParams(raw: String): Map<String, String> {
            val out = HashMap<String, String>()
            var i = 0
            while (i < raw.length) {
                while (i < raw.length && (raw[i] == ' ' || raw[i] == ',' || raw[i] == '\t')) i++
                val eq = raw.indexOf('=', i)
                if (eq <= i) break
                val key = raw.substring(i, eq).trim().lowercase(Locale.US)
                i = eq + 1
                while (i < raw.length && raw[i] == ' ') i++
                val value = if (i < raw.length && raw[i] == '"') {
                    i++
                    val endQuote = raw.indexOf('"', i)
                    if (endQuote < 0) {
                        val rest = raw.substring(i)
                        i = raw.length
                        rest
                    } else {
                        val v = raw.substring(i, endQuote)
                        i = endQuote + 1
                        v
                    }
                } else {
                    val comma = raw.indexOf(',', i)
                    if (comma < 0) {
                        val rest = raw.substring(i).trim()
                        i = raw.length
                        rest
                    } else {
                        val v = raw.substring(i, comma).trim()
                        i = comma + 1
                        v
                    }
                }
                out[key] = value
            }
            return out
        }

        private fun hexDigest(algorithm: String, text: String): String {
            val bytes = MessageDigest.getInstance(algorithm).digest(text.toByteArray(Charsets.UTF_8))
            val sb = StringBuilder(bytes.size * 2)
            for (b in bytes) {
                val v = b.toInt() and 0xff
                sb.append(HEX[v ushr 4]).append(HEX[v and 0x0f])
            }
            return sb.toString()
        }

        private val HEX = "0123456789abcdef".toCharArray()
    }
}

data class RemoteRtpEndpoint(
    val ip: String,
    val port: Int,
    val payloadTypes: List<Int> = listOf(0),
)

object SdpCodec {
    fun buildAudioOffer(localIp: String, rtpPort: Int, sessionId: Long = System.currentTimeMillis()): String {
        val ip = localIp.ifBlank { "0.0.0.0" }
        return buildString {
            append("v=0\r\n")
            append("o=trdriver ").append(sessionId).append(" ").append(sessionId).append(" IN IP4 ").append(ip).append("\r\n")
            append("s=TR Driver LocalTel\r\n")
            append("c=IN IP4 ").append(ip).append("\r\n")
            append("t=0 0\r\n")
            append("m=audio ").append(rtpPort).append(" RTP/AVP 0 8 101\r\n")
            append("a=rtpmap:0 PCMU/8000\r\n")
            append("a=rtpmap:8 PCMA/8000\r\n")
            append("a=rtpmap:101 telephone-event/8000\r\n")
            append("a=fmtp:101 0-16\r\n")
            append("a=ptime:20\r\n")
            append("a=sendrecv\r\n")
        }
    }

    fun parseRemoteAudio(sdp: String): RemoteRtpEndpoint? {
        var sessionIp: String? = null
        var mediaIp: String? = null
        var mediaPort = 0
        val payloadTypes = ArrayList<Int>()
        var inAudioSection = false

        for (rawLine in sdp.replace("\r\n", "\n").split('\n')) {
            val line = rawLine.trim()
            if (line.isEmpty()) continue
            when {
                line.startsWith("c=IN IP4 ") -> {
                    val ip = line.substring(9).substringBefore('/').trim()
                    if (inAudioSection) mediaIp = ip else sessionIp = ip
                }
                line.startsWith("m=audio ") -> {
                    inAudioSection = true
                    val parts = line.substring(8).trim().split(Regex("\\s+"))
                    mediaPort = parts.getOrNull(0)?.toIntOrNull() ?: 0
                    payloadTypes.clear()
                    for (p in parts.drop(2)) {
                        p.toIntOrNull()?.let { payloadTypes.add(it) }
                    }
                }
                line.startsWith("m=") -> {
                    inAudioSection = false
                }
            }
        }

        val ip = (mediaIp ?: sessionIp)?.takeIf { it.isNotBlank() } ?: return null
        if (mediaPort !in 1..65535) return null
        return RemoteRtpEndpoint(
            ip = ip,
            port = mediaPort,
            payloadTypes = payloadTypes.ifEmpty { listOf(0) },
        )
    }
}

object SipMessageBuilder {
    fun buildRequest(
        method: String,
        requestUri: String,
        viaTransport: String,
        localHost: String,
        localPort: Int,
        branch: String,
        fromDisplayName: String,
        fromUri: String,
        fromTag: String,
        toUri: String,
        toTag: String? = null,
        callId: String,
        cseq: Int,
        contactUri: String? = null,
        expiresSec: Int? = null,
        maxForwards: Int = 70,
        authHeaderName: String? = null,
        authHeaderValue: String? = null,
        contentType: String? = null,
        body: String = "",
    ): String {
        val bodyBytes = body.toByteArray(Charsets.UTF_8)
        return buildString {
            append(method).append(' ').append(requestUri).append(" SIP/2.0\r\n")
            append("Via: SIP/2.0/").append(viaTransport.uppercase(Locale.US)).append(' ')
                .append(localHost).append(':').append(localPort)
                .append(";rport;branch=").append(branch).append("\r\n")
            append("Max-Forwards: ").append(maxForwards).append("\r\n")
            append("From: ")
            if (fromDisplayName.isNotBlank()) {
                append('"').append(sanitizeDisplay(fromDisplayName)).append("\" ")
            }
            append('<').append(fromUri).append(">;tag=").append(fromTag).append("\r\n")
            append("To: <").append(toUri).append('>')
            if (!toTag.isNullOrBlank()) append(";tag=").append(toTag)
            append("\r\n")
            append("Call-ID: ").append(callId).append("\r\n")
            append("CSeq: ").append(cseq).append(' ').append(method).append("\r\n")
            if (!contactUri.isNullOrBlank()) {
                append("Contact: <").append(contactUri).append(">\r\n")
            }
            if (expiresSec != null) {
                append("Expires: ").append(expiresSec).append("\r\n")
            }
            if (!authHeaderName.isNullOrBlank() && !authHeaderValue.isNullOrBlank()) {
                append(authHeaderName).append(": ").append(authHeaderValue).append("\r\n")
            }
            append("User-Agent: TRDriver-LocalTel/0.13.0\r\n")
            if (!contentType.isNullOrBlank() && bodyBytes.isNotEmpty()) {
                append("Content-Type: ").append(contentType).append("\r\n")
            }
            append("Content-Length: ").append(bodyBytes.size).append("\r\n\r\n")
            if (bodyBytes.isNotEmpty()) append(body)
        }
    }

    fun buildResponse(
        request: SipMessage,
        statusCode: Int,
        reasonPhrase: String,
        toTag: String,
        contactUri: String? = null,
        contentType: String? = null,
        body: String = "",
    ): String {
        val bodyBytes = body.toByteArray(Charsets.UTF_8)
        return buildString {
            append("SIP/2.0 ").append(statusCode).append(' ').append(reasonPhrase).append("\r\n")
            for (via in request.viaHeaders()) {
                append("Via: ").append(via).append("\r\n")
            }
            append("From: ").append(request.fromHeader()).append("\r\n")
            val rawTo = request.toHeader()
            append("To: ").append(rawTo)
            if (toTag.isNotBlank() && !rawTo.contains(";tag=", ignoreCase = true)) {
                append(";tag=").append(toTag)
            }
            append("\r\n")
            append("Call-ID: ").append(request.callId()).append("\r\n")
            request.header("CSeq")?.let { append("CSeq: ").append(it).append("\r\n") }
            if (!contactUri.isNullOrBlank()) {
                append("Contact: <").append(contactUri).append(">\r\n")
            }
            append("User-Agent: TRDriver-LocalTel/0.13.0\r\n")
            if (!contentType.isNullOrBlank() && bodyBytes.isNotEmpty()) {
                append("Content-Type: ").append(contentType).append("\r\n")
            }
            append("Content-Length: ").append(bodyBytes.size).append("\r\n\r\n")
            if (bodyBytes.isNotEmpty()) append(body)
        }
    }

    fun extractUri(headerValue: String): String {
        val lt = headerValue.indexOf('<')
        val gt = headerValue.lastIndexOf('>')
        if (lt >= 0 && gt > lt) return headerValue.substring(lt + 1, gt).trim()
        return headerValue.substringBefore(';').trim()
    }

    fun extractTag(headerValue: String): String {
        val idx = headerValue.indexOf(";tag=", ignoreCase = true)
        if (idx < 0) return ""
        return headerValue.substring(idx + 5).substringBefore(';').trim()
    }

    private fun sanitizeDisplay(raw: String): String =
        raw.replace("\"", "").replace("\r", "").replace("\n", "").take(64)
}
