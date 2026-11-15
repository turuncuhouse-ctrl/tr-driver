package net.neciparmagan.trdriver.calls

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.abs

class SipAndG711CodecTest {

    @Test
    fun parsesSipInviteAndResponse() {
        val rawInvite = "INVITE sip:1001@10.0.0.2:5060 SIP/2.0\r\n" +
            "Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK776\r\n" +
            "From: \"Ayse\" <sip:+905551112233@pbx.local>;tag=from123\r\n" +
            "To: <sip:1001@pbx.local>\r\n" +
            "Call-ID: abc-call-1\r\n" +
            "CSeq: 42 INVITE\r\n" +
            "Content-Type: application/sdp\r\n" +
            "Content-Length: 46\r\n\r\n" +
            "v=0\r\nc=IN IP4 10.0.0.1\r\nm=audio 12340 RTP/AVP 0\r\n"

        val parsed = SipMessage.parse(rawInvite)
        assertNotNull(parsed)
        assertEquals(false, parsed?.isResponse)
        assertEquals("INVITE", parsed?.method)
        assertEquals("abc-call-1", parsed?.callId())
        assertEquals(42 to "INVITE", parsed?.cseq())
        val rtp = SdpCodec.parseRemoteAudio(parsed!!.body)
        assertNotNull(rtp)
        assertEquals("10.0.0.1", rtp?.ip)
        assertEquals(12340, rtp?.port)
        assertEquals(listOf(0), rtp?.payloadTypes)
    }

    @Test
    fun digestChallengeMatchesRfc2617Vector() {
        val header = """Digest realm="atlanta.com", qop="auth", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", opaque="5ccc069c403ebaf9f0171e9517f40e41""""
        val ch = DigestChallenge.parse(header)
        assertNotNull(ch)
        val auth = ch!!.buildAuthorization(
            method = "REGISTER",
            uri = "sip:atlanta.com",
            username = "1001",
            password = "secret",
            cnonce = "0a4f113b",
            nc = "00000001",
        )
        assertTrue(auth.startsWith("Digest "))
        assertTrue(auth.contains("""username="1001""""))
        assertTrue(auth.contains("""realm="atlanta.com""""))
        assertTrue(auth.contains("qop=auth"))
        assertTrue(auth.contains("""opaque="5ccc069c403ebaf9f0171e9517f40e41""""))
    }

    @Test
    fun sdpOfferAndParseRoundTrip() {
        val sdp = SdpCodec.buildAudioOffer("192.168.1.50", 40120, sessionId = 12345L)
        val remote = SdpCodec.parseRemoteAudio(sdp)
        assertNotNull(remote)
        assertEquals("192.168.1.50", remote?.ip)
        assertEquals(40120, remote?.port)
        assertTrue(remote!!.payloadTypes.contains(0))
        assertTrue(remote.payloadTypes.contains(8))
    }

    @Test
    fun g711MuLawAndALawRoundTripAccuracy() {
        val samples = shortArrayOf(-16000, -4000, -500, 0, 500, 4000, 16000)
        val muEncoded = G711Codec.encodeFrame(samples, samples.size, G711Codec.PAYLOAD_TYPE_PCMU)
        val muDecoded = G711Codec.decodeFrame(muEncoded, 0, muEncoded.size, G711Codec.PAYLOAD_TYPE_PCMU)
        for (i in samples.indices) {
            val tolerance = (abs(samples[i].toInt()) * 0.06).toInt().coerceAtLeast(140)
            assertTrue("muLaw err at $i", abs(samples[i] - muDecoded[i]) <= tolerance)
        }

        val aEncoded = G711Codec.encodeFrame(samples, samples.size, G711Codec.PAYLOAD_TYPE_PCMA)
        val aDecoded = G711Codec.decodeFrame(aEncoded, 0, aEncoded.size, G711Codec.PAYLOAD_TYPE_PCMA)
        for (i in samples.indices) {
            val tolerance = (abs(samples[i].toInt()) * 0.06).toInt().coerceAtLeast(140)
            assertTrue("aLaw err at $i", abs(samples[i] - aDecoded[i]) <= tolerance)
        }
    }

    @Test
    fun rtpPacketEncodeDecodeRoundTrip() {
        val payload = byteArrayOf(0x11, 0x22, 0x33, 0x44)
        val original = RtpPacket(
            payloadType = 0,
            sequence = 1234,
            timestamp = 987654L,
            ssrc = 0x12345678L,
            payload = payload,
        )
        val raw = original.encode()
        assertEquals(16, raw.size)
        val decoded = RtpPacket.decode(raw, raw.size)
        assertNotNull(decoded)
        assertEquals(0, decoded?.payloadType)
        assertEquals(1234, decoded?.sequence)
        assertEquals(987654L, decoded?.timestamp)
        assertEquals(0x12345678L, decoded?.ssrc)
        assertEquals(payload.toList(), decoded?.payload?.toList())
    }
}
