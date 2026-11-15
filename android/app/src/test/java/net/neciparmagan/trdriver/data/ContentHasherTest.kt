package net.neciparmagan.trdriver.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream

class ContentHasherTest {

    @Test
    fun sha256KnownVectors() {
        // NIST vectors, uppercase hex as consumed by the update manifest.
        assertEquals(
            "BA7816BF8F01CFEA414140DE5DAE2223B00361A396177A9CB410FF61F20015AD",
            ContentHasher.sha256Hex(ByteArrayInputStream("abc".toByteArray())),
        )
        assertEquals(
            "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855",
            ContentHasher.sha256Hex(ByteArrayInputStream(ByteArray(0))),
        )
        // Multi-chunk input (buffer is 64 KiB).
        val big = ByteArray(200_000) { (it % 251).toByte() }
        val fromStream = ContentHasher.sha256Hex(ByteArrayInputStream(big))
        val expected = java.security.MessageDigest.getInstance("SHA-256")
            .digest(big)
            .joinToString("") { b -> "%02X".format(b) }
        assertEquals(expected, fromStream)
    }

    @Test
    fun matchesAcceptsBlankExpectedAndCaseDifferences() {
        val actual = "deadbeef"
        // Older manifests have no apkSha256 → nothing to compare.
        assertTrue(ContentHasher.matches(actual, ""))
        assertTrue(ContentHasher.matches(actual, "  "))
        assertTrue(ContentHasher.matches(actual, "DEADBEEF"))
        assertTrue(ContentHasher.matches(actual, "deadbeef"))
        assertFalse(ContentHasher.matches(actual, "cafebabe"))
        assertFalse(ContentHasher.matches(actual, "deadbeef0"))
    }
}
