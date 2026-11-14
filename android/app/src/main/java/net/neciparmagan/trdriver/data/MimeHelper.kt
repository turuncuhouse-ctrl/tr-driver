package net.neciparmagan.trdriver.data

/**
 * Framework-free MIME guesses by file extension.
 *
 * Pure Kotlin (no Android dependencies) so it is covered by JVM unit tests.
 * Used as a fallback when [android.webkit.MimeTypeMap] knows nothing about
 * an extension — e.g. heic on old devices or pdf on stripped-down ROMs.
 */
object MimeHelper {
    fun guessFromExtension(ext: String): String? {
        return when (ext.lowercase()) {
            "jpg", "jpeg" -> "image/jpeg"
            "png" -> "image/png"
            "webp" -> "image/webp"
            "gif" -> "image/gif"
            "bmp" -> "image/bmp"
            "heic", "heif" -> "image/heic"
            "pdf" -> "application/pdf"
            "mp4", "m4v" -> "video/mp4"
            "mov" -> "video/quicktime"
            else -> null
        }
    }
}
