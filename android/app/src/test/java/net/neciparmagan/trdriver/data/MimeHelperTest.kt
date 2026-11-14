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
    fun unknownExtensionsReturnNull() {
        assertNull(MimeHelper.guessFromExtension("mp3"))
        assertNull(MimeHelper.guessFromExtension("txt"))
        assertNull(MimeHelper.guessFromExtension(""))
    }
}
