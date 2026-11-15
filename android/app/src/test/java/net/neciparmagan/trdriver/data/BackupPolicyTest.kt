package net.neciparmagan.trdriver.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class BackupPolicyTest {

    // --- classification -----------------------------------------------------

    @Test
    fun kindByMimeType() {
        assertEquals(BackupPolicy.KIND_PHOTO, BackupPolicy.kindOf("image/jpeg", "a.bin"))
        assertEquals(BackupPolicy.KIND_VIDEO, BackupPolicy.kindOf("video/mp4", "a.bin"))
        assertEquals(BackupPolicy.KIND_AUDIO, BackupPolicy.kindOf("audio/mpeg", "a.bin"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf("application/pdf", "a.bin"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf("text/plain", "a.bin"))
        // Parameters after ";" are ignored.
        assertEquals(BackupPolicy.KIND_PHOTO, BackupPolicy.kindOf("image/png; charset=binary", "x"))
    }

    @Test
    fun kindFallsBackToExtension() {
        assertEquals(BackupPolicy.KIND_PHOTO, BackupPolicy.kindOf(null, "PHOTO.JPG"))
        assertEquals(BackupPolicy.KIND_PHOTO, BackupPolicy.kindOf("application/octet-stream", "shot.heic"))
        assertEquals(BackupPolicy.KIND_VIDEO, BackupPolicy.kindOf("", "clip.mkv"))
        assertEquals(BackupPolicy.KIND_AUDIO, BackupPolicy.kindOf(null, "song.flac"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "rapor.pdf"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "tablo.xlsx"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "arsiv.zip"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "kurulum.apk"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "notlar.txt"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "uzantusiz"))
        assertEquals(BackupPolicy.KIND_DOC, BackupPolicy.kindOf(null, "sikistirilmis.7z"))
    }

    // --- toggles ------------------------------------------------------------

    @Test
    fun typeTogglesGateKinds() {
        val all = BackupPolicy.TypeToggles(photo = true, video = true, audio = true, doc = true)
        assertTrue(all.allows(BackupPolicy.KIND_PHOTO))
        assertTrue(all.allows(BackupPolicy.KIND_DOC))

        val photosOnly = BackupPolicy.TypeToggles(photo = true, video = false, audio = false, doc = false)
        assertTrue(photosOnly.allows(BackupPolicy.KIND_PHOTO))
        assertFalse(photosOnly.allows(BackupPolicy.KIND_VIDEO))
        assertFalse(photosOnly.allows(BackupPolicy.KIND_AUDIO))
        assertFalse(photosOnly.allows(BackupPolicy.KIND_DOC))

        // Unknown kinds are treated as documents (default bucket).
        val noDocs = BackupPolicy.TypeToggles(photo = true, video = true, audio = true, doc = false)
        assertFalse(noDocs.allows("something-else"))
    }

    // --- size limits --------------------------------------------------------

    @Test
    fun sizeLimits() {
        assertFalse(BackupPolicy.allowsSize(0))
        assertFalse(BackupPolicy.allowsSize(-5))
        assertTrue(BackupPolicy.allowsSize(1))
        assertTrue(BackupPolicy.allowsSize(BackupPolicy.MAX_FILE_BYTES))
        assertFalse(BackupPolicy.allowsSize(BackupPolicy.MAX_FILE_BYTES + 1))
    }

    // --- name / path safety -------------------------------------------------

    @Test
    fun sanitizeDisplayNameNeutralizesTraversal() {
        // Traversal names collapse to the safe fallback.
        assertEquals("dosya", BackupPolicy.sanitizeDisplayName(".."))
        assertEquals("dosya", BackupPolicy.sanitizeDisplayName("..."))
        assertEquals("dosya", BackupPolicy.sanitizeDisplayName(""))
        assertEquals("dosya", BackupPolicy.sanitizeDisplayName("   "))
        // "../evil.apk" keeps no separator: ".._evil.apk"
        val fromTraversal = BackupPolicy.sanitizeDisplayName("../evil.apk")
        assertFalse(fromTraversal.contains('/'))
        assertFalse(fromTraversal.contains('\\'))
        assertTrue(fromTraversal.endsWith(".apk"))
    }

    @Test
    fun sanitizeDisplayNameReplacesSeparatorsAndControlChars() {
        val out = BackupPolicy.sanitizeDisplayName("a/b\\c:d.txt")
        assertFalse(out.contains('/'))
        assertFalse(out.contains('\\'))
        assertFalse(out.contains(':'))
        assertTrue(out.endsWith(".txt"))

        val ctrl = BackupPolicy.sanitizeDisplayName("x\u0000y\u001fz")
        assertFalse(ctrl.contains('\u0000'))
        assertFalse(ctrl.contains('\u001f'))
        assertTrue(ctrl.startsWith("x"))

        // A path of only traversal segments never survives as a path.
        val onlyTraversal = BackupPolicy.sanitizeDisplayName("../../")
        assertFalse(onlyTraversal.contains('/'))
    }

    @Test
    fun sanitizeDisplayNameTruncatesLongNames() {
        assertEquals(150, BackupPolicy.sanitizeDisplayName("a".repeat(400)).length)
        assertEquals("rapor-2026.pdf", BackupPolicy.sanitizeDisplayName("rapor-2026.pdf"))
    }

    @Test
    fun safeDirectPathAcceptsSharedStorage() {
        assertTrue(BackupPolicy.isSafeDirectPath("/storage/emulated/0/Download"))
        assertTrue(BackupPolicy.isSafeDirectPath("/storage/emulated/0"))
        assertTrue(BackupPolicy.isSafeDirectPath("/sdcard/DCIM"))
        assertTrue(
            BackupPolicy.isSafeDirectPath(
                "/storage/emulated/0/Android/media/com.whatsapp/WhatsApp/Media",
            ),
        )
        assertTrue(BackupPolicy.isSafeDirectPath("/storage/ABCD-1234/Music"))
        // Trailing slash is fine.
        assertTrue(BackupPolicy.isSafeDirectPath("/storage/emulated/0/Movies/"))
    }

    @Test
    fun safeDirectPathRejectsEscapes() {
        assertFalse(BackupPolicy.isSafeDirectPath(""))
        assertFalse(BackupPolicy.isSafeDirectPath("Download"))
        assertFalse(BackupPolicy.isSafeDirectPath("relative/../path"))
        assertFalse(BackupPolicy.isSafeDirectPath("/storage/emulated/0/../../data/data"))
        assertFalse(BackupPolicy.isSafeDirectPath("/data/data/net.neciparmagan.trdriver"))
        assertFalse(BackupPolicy.isSafeDirectPath("/system"))
        assertFalse(BackupPolicy.isSafeDirectPath("/"))
        assertFalse(BackupPolicy.isSafeDirectPath("/storage/emulated/0/.."))
    }

    @Test
    fun serverIdValidation() {
        assertTrue(BackupPolicy.isSafeServerId("123e4567-e89b-12d3-a456-426614174000"))
        assertFalse(BackupPolicy.isSafeServerId(""))
        assertFalse(BackupPolicy.isSafeServerId("../../etc/passwd"))
        assertFalse(BackupPolicy.isSafeServerId("abc"))
        assertFalse(BackupPolicy.isSafeServerId("123e4567e89b12d3a456426614174000"))
    }

    @Test
    fun treeUriMappingAndCoverage() {
        assertEquals(
            "/storage/emulated/0/Download",
            BackupPolicy.treeUriToPath(
                "content://com.android.externalstorage.documents/tree/primary%3ADownload",
            ),
        )
        assertEquals(
            "/storage/emulated/0/Android/media/com.whatsapp/WhatsApp/Media",
            BackupPolicy.treeUriToPath(
                "content://com.android.externalstorage.documents/tree/" +
                    "primary%3AAndroid%2Fmedia%2Fcom.whatsapp%2FWhatsApp%2FMedia",
            ),
        )
        // Non-primary volumes (SD cards) are not convertible.
        assertNull(
            BackupPolicy.treeUriToPath(
                "content://com.android.externalstorage.documents/tree/ABCD-1234%3ABackup",
            ),
        )
        assertNull(BackupPolicy.treeUriToPath("content://other/provider"))
        assertNull(BackupPolicy.treeDocumentIdToPath("primary:../data"))

        val presets = listOf("/storage/emulated/0/Download")
        assertTrue(
            BackupPolicy.pathCoveredByPreset("/storage/emulated/0/Download", presets),
        )
        assertTrue(
            BackupPolicy.pathCoveredByPreset("/storage/emulated/0/Download/sub", presets),
        )
        assertFalse(
            BackupPolicy.pathCoveredByPreset("/storage/emulated/0/DownloadOld", presets),
        )
        assertFalse(BackupPolicy.pathCoveredByPreset(null, presets))
        assertFalse(BackupPolicy.pathCoveredByPreset("/storage/emulated/0/DCIM", presets))
    }

    // --- presets ------------------------------------------------------------

    @Test
    fun presetsCoverRequiredFolders() {
        val presets = BackupPolicy.presets()
        assertEquals(8, presets.size)
        val labels = presets.map { it.label }
        assertEquals(labels.distinct(), labels)
        val paths = presets.map { it.path }
        assertEquals(paths.distinct(), paths)
        for (p in presets) {
            assertTrue("path unsafe: ${p.path}", BackupPolicy.isSafeDirectPath(p.path))
            assertTrue(p.treeDocumentId.startsWith("primary:"))
            assertFalse(p.treeDocumentId.contains(".."))
        }
        val byLabel = presets.associateBy { it.label }
        assertTrue(byLabel.containsKey("Download"))
        assertTrue(byLabel.containsKey("DCIM"))
        assertTrue(byLabel.containsKey("Pictures"))
        assertTrue(byLabel.containsKey("Documents"))
        assertTrue(byLabel.containsKey("Music"))
        assertTrue(byLabel.containsKey("Movies"))
        assertEquals(
            "/storage/emulated/0/Android/media/com.whatsapp/WhatsApp/Media",
            byLabel.getValue("WhatsApp Medya").path,
        )
        assertEquals(
            "/storage/emulated/0/Android/media/com.whatsapp/WhatsApp",
            byLabel.getValue("WhatsApp Yedek").path,
        )
    }
}
