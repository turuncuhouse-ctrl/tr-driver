package net.neciparmagan.trdriver.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class MimeHelperTest {
    @Test
    fun knownExtensions() {
        assertEquals("image/jpeg", MimeHelper.guessFromExtension("jpg"))
        assertEquals("image/jpeg", MimeHelper.guessFromExtension("JPEG"))
        assertEquals("image/png", MimeHelper.guessFromExtension("png"))
        assertEquals("image/heic", MimeHelper.guessFromExtension("heic"))
        assertEquals("application/pdf", MimeHelper.guessFromExtension("pdf"))
        assertEquals("video/mp4", MimeHelper.guessFromExtension("mp4"))
        assertEquals("video/quicktime", MimeHelper.guessFromExtension("mov"))
    }

    @Test
    fun audioExtensions() {
        assertEquals("audio/mpeg", MimeHelper.guessFromExtension("mp3"))
        assertEquals("audio/mp4", MimeHelper.guessFromExtension("m4a"))
        assertEquals("audio/wav", MimeHelper.guessFromExtension("wav"))
        assertEquals("audio/flac", MimeHelper.guessFromExtension("flac"))
        assertEquals("audio/ogg", MimeHelper.guessFromExtension("ogg"))
        assertEquals("audio/ogg", MimeHelper.guessFromExtension("opus"))
        assertEquals("audio/aac", MimeHelper.guessFromExtension("aac"))
    }

    @Test
    fun documentAndArchiveExtensions() {
        assertEquals("application/msword", MimeHelper.guessFromExtension("doc"))
        assertEquals(
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            MimeHelper.guessFromExtension("docx"),
        )
        assertEquals("application/vnd.ms-excel", MimeHelper.guessFromExtension("xls"))
        assertEquals(
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
            MimeHelper.guessFromExtension("xlsx"),
        )
        assertEquals("text/plain", MimeHelper.guessFromExtension("txt"))
        assertEquals("text/csv", MimeHelper.guessFromExtension("csv"))
        assertEquals("application/zip", MimeHelper.guessFromExtension("zip"))
        assertEquals("application/vnd.rar", MimeHelper.guessFromExtension("rar"))
        assertEquals("application/x-7z-compressed", MimeHelper.guessFromExtension("7z"))
        assertEquals("application/vnd.android.package-archive", MimeHelper.guessFromExtension("apk"))
        assertEquals("video/x-matroska", MimeHelper.guessFromExtension("mkv"))
        assertEquals("video/webm", MimeHelper.guessFromExtension("webm"))
    }

    @Test
    fun unknownExtensionsReturnNull() {
        assertNull(MimeHelper.guessFromExtension("xyz"))
        assertNull(MimeHelper.guessFromExtension("db"))
        assertNull(MimeHelper.guessFromExtension(""))
        assertNull(MimeHelper.guessFromExtension("noext"))
    }
}
