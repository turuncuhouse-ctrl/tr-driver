package net.neciparmagan.trdriver.data

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import androidx.core.content.FileProvider
import java.io.File
import java.io.IOException

data class CachedUpload(
    val uri: Uri,
    val displayName: String,
    val mimeType: String,
    val sizeBytes: Long,
    val localFile: File?,
)

object UriCacheCopy {
    fun copyToCache(context: Context, uri: Uri, subdir: String = "share_import"): CachedUpload {
        val resolver = context.contentResolver
        var name = "dosya_${System.currentTimeMillis()}"
        resolver.query(uri, null, null, null, null)?.use { cursor ->
            val nameIdx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            if (cursor.moveToFirst() && nameIdx >= 0) {
                name = cursor.getString(nameIdx)?.ifBlank { name } ?: name
            }
        }
        val mime = resolver.getType(uri) ?: guessMime(name)
        val safe = name.replace(Regex("""[\\/:*?"<>|]"""), "_").ifBlank { "dosya.bin" }
        val dir = File(context.cacheDir, subdir).also { it.mkdirs() }
        val dest = File(dir, "${System.currentTimeMillis()}_$safe")
        resolver.openInputStream(uri)?.use { input ->
            dest.outputStream().use { output -> input.copyTo(output) }
        } ?: throw IOException("Dosya okunamadı (Bluetooth/ indirme izni)")
        if (!dest.exists() || dest.length() <= 0L) {
            dest.delete()
            throw IOException("Dosya boş veya kopyalanamadı")
        }
        val shareUri = FileProvider.getUriForFile(context, "${context.packageName}.files", dest)
        return CachedUpload(
            uri = shareUri,
            displayName = safe,
            mimeType = mime,
            sizeBytes = dest.length(),
            localFile = dest,
        )
    }

    private fun guessMime(name: String): String {
        val ext = name.substringAfterLast('.', "").lowercase()
        return when (ext) {
            "jpg", "jpeg" -> "image/jpeg"
            "png" -> "image/png"
            "webp" -> "image/webp"
            "gif" -> "image/gif"
            "pdf" -> "application/pdf"
            "mp4" -> "video/mp4"
            else -> "application/octet-stream"
        }
    }
}
