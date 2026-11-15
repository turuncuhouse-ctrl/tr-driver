package net.neciparmagan.trdriver.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class IncrementalPolicyTest {

    private val hashA = "aa".repeat(32)
    private val hashB = "bb".repeat(32)

    @Test
    fun newFileIsUploaded() {
        assertEquals(
            IncrementalPolicy.Decision.NEW,
            IncrementalPolicy.decide(null, 100L, 1_000L),
        )
        assertFalse(IncrementalPolicy.needsContentHash(null, 100L, 1_000L))
    }

    @Test
    fun unchangedFileSkipsWithoutReadingHash() {
        val stored = IncrementalPolicy.Stored(100L, 1_000L, hashA)
        assertEquals(
            IncrementalPolicy.Decision.SKIP,
            IncrementalPolicy.decide(stored, 100L, 1_000L),
        )
        assertFalse(IncrementalPolicy.needsContentHash(stored, 100L, 1_000L))
    }

    @Test
    fun sizeChangeUploads() {
        val stored = IncrementalPolicy.Stored(100L, 1_000L, hashA)
        assertEquals(
            IncrementalPolicy.Decision.UPLOAD,
            IncrementalPolicy.decide(stored, 200L, 1_000L),
        )
        // Size differs → no need to read the hash.
        assertFalse(IncrementalPolicy.needsContentHash(stored, 200L, 2_000L))
    }

    @Test
    fun mtimeChangeWithSameHashSkips() {
        val stored = IncrementalPolicy.Stored(100L, 1_000L, hashA)
        assertTrue(IncrementalPolicy.needsContentHash(stored, 100L, 2_000L))
        assertEquals(
            IncrementalPolicy.Decision.SKIP,
            IncrementalPolicy.decide(stored, 100L, 2_000L, hashA),
        )
    }

    @Test
    fun mtimeChangeWithDifferentHashUploads() {
        val stored = IncrementalPolicy.Stored(100L, 1_000L, hashA)
        assertEquals(
            IncrementalPolicy.Decision.UPLOAD,
            IncrementalPolicy.decide(stored, 100L, 2_000L, hashB),
        )
        // Hash comparison is case-insensitive (hex formatting differences).
        assertEquals(
            IncrementalPolicy.Decision.SKIP,
            IncrementalPolicy.decide(stored, 100L, 2_000L, hashA.uppercase()),
        )
    }

    @Test
    fun hashNeededOnlyWhenStoredHashExists() {
        // Legacy row without a hash: the decision never needs a hash read.
        val legacy = IncrementalPolicy.Stored(100L, -1L, "")
        assertFalse(IncrementalPolicy.needsContentHash(legacy, 100L, 2_000L))
        val legacyWithKnownMtime = IncrementalPolicy.Stored(100L, 1_000L, "")
        assertFalse(IncrementalPolicy.needsContentHash(legacyWithKnownMtime, 100L, 2_000L))
    }

    @Test
    fun legacyRowAdoptsMetadataInsteadOfReuploading() {
        // Pre-0.10 rows have neither mtime nor hash: adopting them keeps the
        // upgrade from re-uploading the entire library.
        val legacy = IncrementalPolicy.Stored(100L, -1L, "")
        assertEquals(
            IncrementalPolicy.Decision.ADOPT_META,
            IncrementalPolicy.decide(legacy, 100L, 5_000L),
        )
    }

    @Test
    fun legacyRowWithKnownMtimeUploadsOnce() {
        // A pre-hash row whose mtime later changes can't be proven identical,
        // so it uploads once — after that the hash is recorded forever.
        val legacy = IncrementalPolicy.Stored(100L, 1_000L, "")
        assertEquals(
            IncrementalPolicy.Decision.UPLOAD,
            IncrementalPolicy.decide(legacy, 100L, 2_000L),
        )
        // Same mtime → cheap skip regardless of hash.
        assertEquals(
            IncrementalPolicy.Decision.SKIP,
            IncrementalPolicy.decide(legacy, 100L, 1_000L),
        )
    }

    @Test
    fun undecidedHashCallIsDocumentedAsSkip() {
        // Caller contract: ask needsContentHash() first, then re-invoke with
        // the computed hash. Passing null reports SKIP (never re-uploads
        // blindly) — covered here so the contract can't drift silently.
        val stored = IncrementalPolicy.Stored(100L, 1_000L, hashA)
        assertTrue(IncrementalPolicy.needsContentHash(stored, 100L, 2_000L))
        assertEquals(
            IncrementalPolicy.Decision.SKIP,
            IncrementalPolicy.decide(stored, 100L, 2_000L, null),
        )
    }

    @Test
    fun storedRowCarriesRecordedValues() {
        val s = IncrementalPolicy.Stored(sizeBytes = 42L, mtimeMs = -1L, contentHash = "")
        assertEquals(42L, s.sizeBytes)
        assertEquals(-1L, s.mtimeMs)
        assertEquals("", s.contentHash)
    }
}
