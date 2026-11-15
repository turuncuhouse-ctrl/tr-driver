package net.neciparmagan.trdriver.data

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SharedSpacePathsTest {
    @Test
    fun joinAndParentAreInverse() {
        assertEquals("Filmler", SharedSpacePaths.join("", "Filmler"))
        assertEquals("Filmler/2024", SharedSpacePaths.join("Filmler", "2024"))
        assertEquals("Filmler", SharedSpacePaths.parent("Filmler/2024"))
        assertEquals("", SharedSpacePaths.parent("Filmler"))
        assertEquals("", SharedSpacePaths.parent(""))
        assertEquals("film.mkv", SharedSpacePaths.nameOf("Filmler/2024/film.mkv"))
        assertEquals("Filmler", SharedSpacePaths.nameOf("Filmler"))
    }

    @Test
    fun writeLevels() {
        assertTrue(SharedSpacePaths.canWrite("write"))
        assertTrue(SharedSpacePaths.canWrite("admin"))
        assertFalse(SharedSpacePaths.canWrite("read"))
        assertFalse(SharedSpacePaths.canWrite("none"))
        assertFalse(SharedSpacePaths.canWrite(""))
    }

    @Test
    fun namesThatCouldLeaveTheFolderAreRejected() {
        for (bad in listOf("", "   ", ".", "..", "a/b", "..\\x", "a\\b", "satir\nsonu", "nul\u0000", "x".repeat(256))) {
            assertFalse("should reject: $bad", SharedSpacePaths.isValidName(bad))
        }
        for (good in listOf("rapor.pdf", "Bölüm 1.mkv", "..gizli", "a..b", ".env", "x".repeat(255))) {
            assertTrue("should accept: $good", SharedSpacePaths.isValidName(good))
        }
    }

    @Test
    fun cacheNameIsNeverAPath() {
        assertEquals("rapor.pdf", SharedSpacePaths.cacheFileName("rapor.pdf"))
        assertEquals(".._.._etc_passwd", SharedSpacePaths.cacheFileName("../../etc/passwd"))
        assertEquals("a_b", SharedSpacePaths.cacheFileName("a\\b"))
        assertEquals("indirilen.bin", SharedSpacePaths.cacheFileName(".."))
        assertEquals("indirilen.bin", SharedSpacePaths.cacheFileName("  "))
        assertFalse(SharedSpacePaths.cacheFileName("x/../../y").contains('/'))
    }

    @Test
    fun sizesAreReadable() {
        assertEquals("0 B", SharedSpacePaths.formatSize(0))
        assertEquals("512 B", SharedSpacePaths.formatSize(512))
        assertEquals("1.0 KB", SharedSpacePaths.formatSize(1024))
        assertEquals("1.5 MB", SharedSpacePaths.formatSize(1024L * 1024 * 3 / 2))
        assertEquals("2.0 GB", SharedSpacePaths.formatSize(2L * 1024 * 1024 * 1024))
    }

    @Test
    fun requestBodiesCarryExactlyTheFieldsTheServerAccepts() {
        val json = Json { ignoreUnknownKeys = true }
        // The root folder is the empty path and must still be sent.
        assertEquals(
            """{"path":"","name":"Yeni"}""",
            json.encodeToString(SharedPathRequest.serializer(), SharedPathRequest(path = "", name = "Yeni")),
        )
        // Delete has no name field: the server refuses unknown fields.
        assertEquals(
            """{"path":"Filmler/eski.txt"}""",
            json.encodeToString(SharedDeleteRequest.serializer(), SharedDeleteRequest(path = "Filmler/eski.txt")),
        )
    }

    @Test
    fun serverResponsesDecode() {
        val json = Json { ignoreUnknownKeys = true }
        val listing = json.decodeFromString(
            SharedListing.serializer(),
            """{"path":"Filmler","level":"read","entries":[{"name":"film.mkv","path":"Filmler/film.mkv","isDir":false,"size":42,"modifiedAt":"2026-11-15T12:00:00Z","level":"read"}]}""",
        )
        assertEquals("Filmler", listing.path)
        assertEquals(1, listing.entries?.size)
        assertEquals(42L, listing.entries?.first()?.size)

        // A missing or null list must not break decoding.
        val empty = json.decodeFromString(SharedListing.serializer(), """{"path":"","level":"none","entries":null}""")
        assertNull(empty.entries)

        // No access: there is no quota object at all.
        val status = json.decodeFromString(
            SharedStatus.serializer(),
            """{"access":{"available":false,"rootLevel":"none","maxLevel":"none","admin":false}}""",
        )
        assertFalse(status.access.available)
        assertNull(status.quota)

        val full = json.decodeFromString(
            SharedStatus.serializer(),
            """{"access":{"available":true,"rootLevel":"write","maxLevel":"write","admin":false},"quota":{"quotaBytes":100,"limitBytes":100,"usedBytes":100,"freeBytes":0,"diskTotalBytes":5,"usedPercent":100,"warnPercent":90,"level":"full"}}""",
        )
        assertEquals("full", full.quota?.level)
        assertTrue(SharedSpacePaths.canWrite(full.access.rootLevel))
    }
}
