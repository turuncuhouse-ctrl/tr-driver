package net.neciparmagan.trdriver.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PersonalBackupPolicyTest {

    private val hashA = "aa".repeat(32)
    private val hashB = "bb".repeat(32)

    // --- identity -----------------------------------------------------------

    @Test
    fun phoneIdentityIgnoresFormattingAndCountryPrefix() {
        assertEquals("5551112233", PersonalBackupPolicy.phoneIdentity("+90 (555) 111 22 33"))
        assertEquals("5551112233", PersonalBackupPolicy.phoneIdentity("0555 111 22 33"))
        assertEquals("5551112233", PersonalBackupPolicy.phoneIdentity("+90-555-111-2233"))
        assertNull(PersonalBackupPolicy.phoneIdentity(""))
        assertNull(PersonalBackupPolicy.phoneIdentity(null))
        // Short codes are not an identity.
        assertNull(PersonalBackupPolicy.phoneIdentity("112"))
    }

    @Test
    fun contactKeyIsStableAcrossFormatting() {
        val a = PersonalBackupPolicy.contactKey("Ayşe", listOf("+90 555 111 22 33"), emptyList())
        val b = PersonalBackupPolicy.contactKey("Ayşe Yılmaz", listOf("0555-111-2233"), emptyList())
        assertEquals(a, b)
        assertTrue(PersonalBackupPolicy.isSafeKey(a))
    }

    @Test
    fun contactKeyFallsBackToEmailThenName() {
        val byMail = PersonalBackupPolicy.contactKey("Ayşe", emptyList(), listOf("AYSE@Example.com"))
        val byMailLower = PersonalBackupPolicy.contactKey("Different name", emptyList(), listOf("ayse@example.com"))
        assertEquals(byMail, byMailLower)

        val byName = PersonalBackupPolicy.contactKey("  Ayşe   Yılmaz ", emptyList(), emptyList())
        val byNameSame = PersonalBackupPolicy.contactKey("ayşe yılmaz", emptyList(), emptyList())
        assertEquals(byName, byNameSame)
        assertNotEquals(byName, byMail)
    }

    @Test
    fun differentContactsGetDifferentKeys() {
        val first = PersonalBackupPolicy.contactKey("A", listOf("+905551112233"), emptyList())
        val second = PersonalBackupPolicy.contactKey("B", listOf("+905554445566"), emptyList())
        assertNotEquals(first, second)
        assertTrue(PersonalBackupPolicy.isSafeKey(first))
        assertEquals(PersonalBackupPolicy.KEY_LENGTH, first.length)
    }

    @Test
    fun smsKeyIsStableAndIdScoped() {
        val key = PersonalBackupPolicy.smsKey(42L, 1_700_000_000_000L, "+90 555 111 22 33")
        assertEquals(
            key,
            PersonalBackupPolicy.smsKey(42L, 1_700_000_000_000L, "0555 111 2233"),
        )
        assertNotEquals(key, PersonalBackupPolicy.smsKey(43L, 1_700_000_000_000L, "+905551112233"))
        assertNotEquals(key, PersonalBackupPolicy.smsKey(42L, 1_700_000_000_001L, "+905551112233"))
        assertTrue(PersonalBackupPolicy.isSafeKey(key))
    }

    @Test
    fun unsafeKeysAreRejected() {
        assertFalse(PersonalBackupPolicy.isSafeKey(""))
        assertFalse(PersonalBackupPolicy.isSafeKey("short-key"))
        assertFalse(PersonalBackupPolicy.isSafeKey("a".repeat(PersonalBackupPolicy.KEY_LENGTH) + "/"))
    }

    // --- incremental decisions ---------------------------------------------

    @Test
    fun newRecordIsSent() {
        assertEquals(
            PersonalBackupPolicy.Decision.NEW,
            PersonalBackupPolicy.decide(null, 100L, hashA),
        )
    }

    @Test
    fun unchangedHashSkipsEvenWhenRevisionMoved() {
        val stored = PersonalBackupPolicy.Stored(revision = 100L, hash = hashA)
        assertEquals(
            PersonalBackupPolicy.Decision.SKIP,
            PersonalBackupPolicy.decide(stored, 200L, hashA),
        )
    }

    @Test
    fun changedContentIsUpdated() {
        val stored = PersonalBackupPolicy.Stored(revision = 100L, hash = hashA)
        assertEquals(
            PersonalBackupPolicy.Decision.UPDATE,
            PersonalBackupPolicy.decide(stored, 200L, hashB),
        )
    }

    @Test
    fun olderRevisionThanServerIsSkipped() {
        val stored = PersonalBackupPolicy.Stored(revision = 500L, hash = hashA)
        // Different content but the server already has a newer revision.
        assertEquals(
            PersonalBackupPolicy.Decision.SKIP,
            PersonalBackupPolicy.decide(stored, 100L, hashB),
        )
        // Equal revision with different content is still pushed (last writer wins).
        assertEquals(
            PersonalBackupPolicy.Decision.UPDATE,
            PersonalBackupPolicy.decide(stored, 500L, hashB),
        )
    }

    @Test
    fun emptyStoredHashNeverMatches() {
        val stored = PersonalBackupPolicy.Stored(revision = 0L, hash = "")
        assertEquals(
            PersonalBackupPolicy.Decision.UPDATE,
            PersonalBackupPolicy.decide(stored, 0L, ""),
        )
    }

    // --- deletion safety ----------------------------------------------------

    @Test
    fun deletionsOnlyWhenScanIsCompleteAndNonEmpty() {
        val stored = setOf("a", "b", "c")
        assertEquals(listOf("b", "c"), PersonalBackupPolicy.pendingDeletions(stored, setOf("a"), true))
        assertTrue(PersonalBackupPolicy.pendingDeletions(stored, setOf("a"), false).isEmpty())
        assertTrue(PersonalBackupPolicy.pendingDeletions(stored, emptySet(), true).isEmpty())
        assertTrue(PersonalBackupPolicy.pendingDeletions(emptySet(), setOf("a"), true).isEmpty())
    }

    @Test
    fun deletionsAreSortedForKeyStability() {
        val stored = setOf("z", "m", "a")
        assertEquals(listOf("a", "m", "z"), PersonalBackupPolicy.pendingDeletions(stored, setOf("keep"), true))
    }

    // --- batching and limits ------------------------------------------------

    @Test
    fun chunkSplitsWithoutLosingItems() {
        val items = (1..450).toList()
        val batches = PersonalBackupPolicy.chunk(items, 200)
        assertEquals(3, batches.size)
        assertEquals(items, batches.flatten())
        assertTrue(PersonalBackupPolicy.chunk(emptyList<String>(), 200).isEmpty())
        assertEquals(1, PersonalBackupPolicy.chunk(listOf("x"), 0).size)
    }

    @Test
    fun chunkSizesStayInsideServerLimits() {
        assertEquals(200, PersonalBackupPolicy.chunkSize(PersonalBackupPolicy.KIND_CONTACTS))
        assertEquals(100, PersonalBackupPolicy.chunkSize(PersonalBackupPolicy.KIND_SMS))
        assertTrue(PersonalBackupPolicy.chunkSize(PersonalBackupPolicy.KIND_CONTACTS) <= 500)
        assertTrue(PersonalBackupPolicy.maxItemBytes(PersonalBackupPolicy.KIND_CONTACTS) <= 16 * 1024)
        assertTrue(PersonalBackupPolicy.maxItemBytes(PersonalBackupPolicy.KIND_SMS) <= 64 * 1024)
    }

    @Test
    fun itemSizeLimitsAreEnforced() {
        assertTrue(PersonalBackupPolicy.allowsItem(PersonalBackupPolicy.KIND_CONTACTS, 100))
        assertFalse(PersonalBackupPolicy.allowsItem(PersonalBackupPolicy.KIND_CONTACTS, 0))
        assertFalse(
            PersonalBackupPolicy.allowsItem(
                PersonalBackupPolicy.KIND_CONTACTS,
                PersonalBackupPolicy.maxItemBytes(PersonalBackupPolicy.KIND_CONTACTS) + 1,
            ),
        )
        assertTrue(PersonalBackupPolicy.allowsItem(PersonalBackupPolicy.KIND_SMS, 60 * 1024))
    }

    @Test
    fun contentHashIsHexSha256() {
        val hash = PersonalBackupPolicy.contentHash("abc")
        assertEquals(64, hash.length)
        assertTrue(hash.all { it in '0'..'9' || it in 'a'..'f' })
        // Known SHA-256 of "abc".
        assertEquals(
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
            hash,
        )
        assertNotEquals(hash, PersonalBackupPolicy.contentHash("abd"))
    }
}
