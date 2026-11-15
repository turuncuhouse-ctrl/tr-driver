package net.neciparmagan.trdriver.calls

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PhoneNumberNormalizerTest {

    @Test
    fun normalizesTurkishMobileFormatsConsistently() {
        val formats = listOf(
            "+905551112233",
            "+90 (555) 111 22 33",
            "0090 555-111-22-33",
            "0555 111 22 33",
            "5551112233",
            "905551112233",
            "tel:+905551112233",
            "sip:05551112233@pbx.localtel.net;user=phone",
        )
        for (raw in formats) {
            val norm = PhoneNumberNormalizer.normalize(raw, defaultCountryCode = "90")
            assertEquals("E.164 mismatch for $raw", "+905551112233", norm.e164)
            assertEquals("Match key mismatch for $raw", "5551112233", norm.matchKey)
            assertEquals("Country code mismatch for $raw", "90", norm.countryCode)
            assertEquals("+90 555 111 22 33", norm.displayNumber)
            assertTrue(norm.isDialable)
        }
    }

    @Test
    fun normalizesTurkishLandlineAndSpecialNumbers() {
        val izmir = PhoneNumberNormalizer.normalize("0232 123 45 67", "90")
        assertEquals("+902321234567", izmir.e164)
        assertEquals("2321234567", izmir.matchKey)
        assertEquals("90", izmir.countryCode)

        val fourFourFour = PhoneNumberNormalizer.normalize("444 0 123", "90")
        assertNull(fourFourFour.e164)
        assertEquals("4440123", fourFourFour.matchKey)
        assertEquals("444 01 23", fourFourFour.displayNumber)
    }

    @Test
    fun preservesShortExtensionsSeparatelyFromE164() {
        val ext = PhoneNumberNormalizer.normalize("1001", "90")
        assertNull(ext.e164)
        assertNull(ext.matchKey)
        assertEquals("1001", ext.extensionKey)
        assertEquals("1001", ext.displayNumber)
        assertTrue(ext.isDialable)
    }

    @Test
    fun parsesInternationalAndSipUrisWithDisplayNameHint() {
        val us = PhoneNumberNormalizer.normalize("+1 (509) 888-8888", "90")
        assertEquals("+15098888888", us.e164)
        assertEquals("1", us.countryCode)
        assertEquals("5098888888", us.matchKey)
        assertEquals("+1 (509) 888-8888", us.displayNumber)

        val sipFrom = PhoneNumberNormalizer.normalize("\"Ayşe Yılmaz\" <sip:05551112233@10.0.0.5:5060>", "90")
        assertEquals("+905551112233", sipFrom.e164)
        assertEquals("Ayşe Yılmaz", sipFrom.displayNameHint)

        val sipUser = PhoneNumberNormalizer.normalize("\"Servis\" <sip:servis.hat@pbx.local>", "90")
        assertEquals("servis.hat@pbx.local", sipUser.sipUser)
        assertEquals("Servis", sipUser.displayNameHint)
        assertNull(sipUser.e164)
    }

    @Test
    fun rejectsEmptyOrPunctuationOnlyInput() {
        assertFalse(PhoneNumberNormalizer.normalize("", "90").isDialable)
        assertFalse(PhoneNumberNormalizer.normalize("   ---  ", "90").isDialable)
    }
}
