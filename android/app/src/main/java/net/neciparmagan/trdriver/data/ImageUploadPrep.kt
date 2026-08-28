package net.neciparmagan.trdriver.data

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Matrix
import android.net.Uri
import androidx.core.content.FileProvider
import androidx.exifinterface.media.ExifInterface
import java.io.File
import java.io.FileOutputStream
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

/**
 * Shrinks large camera photos before upload (typical 3–8 MB → ~300–900 KB).
 * Skips GIF/SVG and files already under ~400 KB.
 */
object ImageUploadPrep {
    private const val MAX_EDGE = 2048
    private const val JPEG_QUALITY = 82
    private const val SKIP_BELOW_BYTES = 400_000L

    data class Prepared(
        val uri: Uri,
        val displayName: String,
        val mimeType: String,
        val sizeBytes: Long,
        val tempFile: File?,
    )

    fun prepare(
        context: Context,
        uri: Uri,
        displayName: String,
        mimeType: String,
        sizeHint: Long = -1L,
        subdir: String = "upload_prep",
    ): Prepared {
        val mime = mimeType.ifBlank { guessMime(displayName) }
        if (!shouldCompress(mime, displayName, sizeHint)) {
            return Prepared(uri, displayName, mime, sizeHint, null)
        }
        return runCatching {
            compress(context, uri, displayName, mime, subdir)
        }.getOrElse {
            Prepared(uri, displayName, mime, sizeHint, null)
        }
    }

    private fun shouldCompress(mime: String, name: String, sizeHint: Long): Boolean {
        if (!mime.startsWith("image/")) return false
        if (mime == "image/gif" || mime == "image/svg+xml") return false
        val ext = name.substringAfterLast('.', "").lowercase()
        if (ext == "gif" || ext == "svg") return false
        if (sizeHint in 1 until SKIP_BELOW_BYTES) return false
        return true
    }

    private fun compress(
        context: Context,
        uri: Uri,
        displayName: String,
        mime: String,
        subdir: String,
    ): Prepared {
        val resolver = context.contentResolver
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        resolver.openInputStream(uri)?.use { BitmapFactory.decodeStream(it, null, bounds) }
            ?: return Prepared(uri, displayName, mime, -1L, null)

        val sample = calculateInSampleSize(bounds.outWidth, bounds.outHeight, MAX_EDGE)
        val exifOrientation = resolver.openInputStream(uri)?.use { stream ->
            ExifInterface(stream).getAttributeInt(
                ExifInterface.TAG_ORIENTATION,
                ExifInterface.ORIENTATION_NORMAL,
            )
        } ?: ExifInterface.ORIENTATION_NORMAL

        val decodeOpts = BitmapFactory.Options().apply {
            inSampleSize = sample
            inPreferredConfig = Bitmap.Config.ARGB_8888
        }
        var bitmap = resolver.openInputStream(uri)?.use {
            BitmapFactory.decodeStream(it, null, decodeOpts)
        } ?: return Prepared(uri, displayName, mime, -1L, null)

        bitmap = applyExifRotation(exifOrientation, bitmap)

        val scaled = scaleDown(bitmap, MAX_EDGE)
        if (scaled !== bitmap) bitmap.recycle()

        val safeStem = displayName.substringBeforeLast('.')
            .replace(Regex("""[\\/:*?"<>|]"""), "_")
            .ifBlank { "photo" }
        val dir = File(context.cacheDir, subdir).also { it.mkdirs() }
        val dest = File(dir, "${System.currentTimeMillis()}_$safeStem.jpg")
        FileOutputStream(dest).use { out ->
            scaled.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, out)
        }
        scaled.recycle()

        if (!dest.exists() || dest.length() <= 0L) {
            dest.delete()
            return Prepared(uri, displayName, mime, -1L, null)
        }

        val outName = if (displayName.contains('.')) {
            "${displayName.substringBeforeLast('.')}.jpg"
        } else {
            "$displayName.jpg"
        }
        val shareUri = FileProvider.getUriForFile(context, "${context.packageName}.files", dest)
        return Prepared(
            uri = shareUri,
            displayName = outName,
            mimeType = "image/jpeg",
            sizeBytes = dest.length(),
            tempFile = dest,
        )
    }

    private fun calculateInSampleSize(width: Int, height: Int, maxEdge: Int): Int {
        if (width <= 0 || height <= 0) return 1
        var sample = 1
        var halfW = width / 2
        var halfH = height / 2
        while (halfW / sample >= maxEdge || halfH / sample >= maxEdge) {
            sample *= 2
        }
        return sample.coerceAtLeast(1)
    }

    private fun scaleDown(source: Bitmap, maxEdge: Int): Bitmap {
        val w = source.width
        val h = source.height
        val longest = max(w, h)
        if (longest <= maxEdge) return source
        val scale = maxEdge.toFloat() / longest.toFloat()
        val nw = max(1, (w * scale).roundToInt())
        val nh = max(1, (h * scale).roundToInt())
        return Bitmap.createScaledBitmap(source, nw, nh, true)
    }

    private fun applyExifRotation(orientation: Int, bitmap: Bitmap): Bitmap {
        val degrees = when (orientation) {
            ExifInterface.ORIENTATION_ROTATE_90 -> 90f
            ExifInterface.ORIENTATION_ROTATE_180 -> 180f
            ExifInterface.ORIENTATION_ROTATE_270 -> 270f
            else -> return bitmap
        }
        return rotate(bitmap, degrees)
    }

    private fun rotate(bitmap: Bitmap, degrees: Float): Bitmap {
        val matrix = Matrix().apply { postRotate(degrees) }
        val rotated = Bitmap.createBitmap(bitmap, 0, 0, bitmap.width, bitmap.height, matrix, true)
        if (rotated !== bitmap) bitmap.recycle()
        return rotated
    }

    private fun guessMime(name: String): String {
        return when (name.substringAfterLast('.', "").lowercase()) {
            "jpg", "jpeg" -> "image/jpeg"
            "png" -> "image/png"
            "webp" -> "image/webp"
            "gif" -> "image/gif"
            "heic", "heif" -> "image/heic"
            else -> "application/octet-stream"
        }
    }
}
