package net.neciparmagan.trdriver.calls

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

class ContactMatcherTest {

    private val sampleContacts = listOf(
        ContactDirectoryEntry(
            contactId = 1L,
            displayName = "Ahmet Demir",
            starred = false,
            phones = listOf(ContactPhoneEntry("0555 111 22 33", "Cep")),
            sipAddresses = listOf("ahmet@localtel.net"),
        ),
        ContactDirectoryEntry(
            contactId = 2L,
            displayName = "Işık Yılmaz",
            starred = true,
            phones = listOf(
                ContactPhoneEntry("+90 232 444 55 66", "İş"),
                ContactPhoneEntry("101", "Dahili"),
            ),
        ),
        ContactDirectoryEntry(
            contactId = 3L,
            displayName = "Hans Müller",
            starred = false,
            phones = listOf(ContactPhoneEntry("+49 170 1234567", "Cep")),
        ),
        ContactDirectoryEntry(
            contactId = 4L,
            displayName = "Çağrı Merkezi",
            starred = false,
            phones = listOf(ContactPhoneEntry("+90 555 999 01 01", "Ofis")),
        ),
    )

    @Test
    fun matchesIncomingFormatsAgainstContacts() {
        val index = ContactMatcher.buildIndex(sampleContacts, defaultCountryCode = "90")

        val match1 = index.match("+905551112233")
        assertNotNull(match1)
        assertEquals("Ahmet Demir", match1?.displayName)
        assertEquals(MatchConfidence.EXACT_E164, match1?.confidence)
        assertEquals("Cep", match1?.numberLabel)

        val match2 = index.match("sip:5551112233@pbx.localtel.example")
        assertNotNull(match2)
        assertEquals("Ahmet Demir", match2?.displayName)
        assertEquals(MatchConfidence.EXACT_E164, match2?.confidence)
    }

    @Test
    fun matchesExtensionWithoutSuffixFalsePositives() {
        val index = ContactMatcher.buildIndex(sampleContacts, defaultCountryCode = "90")

        val extMatch = index.match("101")
        assertNotNull(extMatch)
        assertEquals("Işık Yılmaz", extMatch?.displayName)
        assertEquals(MatchConfidence.EXTENSION, extMatch?.confidence)

        // Extension 909 should never match a substring inside a long phone number.
        assertNull(index.match("909"))
    }

    @Test
    fun preventsCrossCountrySuffixFalseMatches() {
        val contacts = listOf(
            ContactDirectoryEntry(
                contactId = 10L,
                displayName = "Berlin Office",
                phones = listOf(ContactPhoneEntry("+49 555 111 22 33")),
            ),
        )
        val index = ContactMatcher.buildIndex(contacts, defaultCountryCode = "90")

        assertNull(index.match("+905551112233"))
        assertNotNull(index.match("0049 555 111 22 33"))
    }

    @Test
    fun matchesSipAddressesAndTurkishSearchFolding() {
        val index = ContactMatcher.buildIndex(sampleContacts, defaultCountryCode = "90")

        val bySip = index.match("sip:ahmet@localtel.net")
        assertEquals("Ahmet Demir", bySip?.displayName)
        assertEquals(MatchConfidence.SIP_URI, bySip?.confidence)

        val searchIsik = index.search("isik")
        assertEquals(1, searchIsik.size)
        assertEquals("Işık Yılmaz", searchIsik.first().displayName)

        val searchCagri = index.search("cagri")
        assertEquals(1, searchCagri.size)
        assertEquals("Çağrı Merkezi", searchCagri.first().displayName)
    }

    @Test
    fun prefersStarredContactWhenNumbersOverlap() {
        val index = ContactMatcher.buildIndex(
            listOf(
                ContactDirectoryEntry(1L, "Normal Kayıt", starred = false, phones = listOf(ContactPhoneEntry("05551112233"))),
                ContactDirectoryEntry(2L, "Favori Kayıt", starred = true, phones = listOf(ContactPhoneEntry("+905551112233"))),
            ),
        )
        val match = index.match("0555 111 22 33")
        assertEquals("Favori Kayıt", match?.displayName)
    }
}
