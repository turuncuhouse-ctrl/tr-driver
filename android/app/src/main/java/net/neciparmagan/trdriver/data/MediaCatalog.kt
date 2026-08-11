package net.neciparmagan.trdriver.data

import android.content.ContentUris
import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import java.util.Calendar

data class LocalMedia(
    val mediaKey: String,
    val uri: Uri,
    val displayName: String,
    val mimeType: String,
    val sizeBytes: Long,
    val dateTakenMs: Long,
    val isVideo: Boolean,
) {
    val year: Int
        get() {
            val c = Calendar.getInstance().apply { timeInMillis = dateTakenMs.coerceAtLeast(0L) }
            return c.get(Calendar.YEAR)
        }
    val month: Int
        get() {
            val c = Calendar.getInstance().apply { timeInMillis = dateTakenMs.coerceAtLeast(0L) }
            return c.get(Calendar.MONTH) + 1
        }
}

object MediaCatalog {
    fun scan(context: Context, limit: Int = 500): List<LocalMedia> {
        val out = ArrayList<LocalMedia>()
        out += query(context, MediaStore.Images.Media.EXTERNAL_CONTENT_URI, false, limit)
        out += query(context, MediaStore.Video.Media.EXTERNAL_CONTENT_URI, true, limit)
        return out.sortedByDescending { it.dateTakenMs }
    }

    private fun query(context: Context, collection: Uri, video: Boolean, limit: Int): List<LocalMedia> {
        val idCol = MediaStore.MediaColumns._ID
        val nameCol = MediaStore.MediaColumns.DISPLAY_NAME
        val mimeCol = MediaStore.MediaColumns.MIME_TYPE
        val sizeCol = MediaStore.MediaColumns.SIZE
        val dateCol = if (Build.VERSION.SDK_INT >= 29) {
            MediaStore.MediaColumns.DATE_TAKEN
        } else {
            MediaStore.MediaColumns.DATE_ADDED
        }
        val projection = arrayOf(idCol, nameCol, mimeCol, sizeCol, dateCol)
        val sort = "$dateCol DESC"
        val result = ArrayList<LocalMedia>()
        context.contentResolver.query(collection, projection, null, null, sort)?.use { c ->
            val iId = c.getColumnIndexOrThrow(idCol)
            val iName = c.getColumnIndexOrThrow(nameCol)
            val iMime = c.getColumnIndexOrThrow(mimeCol)
            val iSize = c.getColumnIndexOrThrow(sizeCol)
            val iDate = c.getColumnIndexOrThrow(dateCol)
            var n = 0
            while (c.moveToNext() && n < limit) {
                val id = c.getLong(iId)
                val name = c.getString(iName) ?: if (video) "video_$id.mp4" else "image_$id.jpg"
                val mime = c.getString(iMime)
                    ?: if (video) "video/mp4" else "image/jpeg"
                val size = c.getLong(iSize)
                var date = c.getLong(iDate)
                if (date > 0 && date < 10_000_000_000L) {
                    // DATE_ADDED is seconds
                    date *= 1000
                }
                if (date <= 0) date = System.currentTimeMillis()
                val uri = ContentUris.withAppendedId(collection, id)
                val volume = collection.toString()
                result += LocalMedia(
                    mediaKey = "$volume/$id",
                    uri = uri,
                    displayName = name,
                    mimeType = mime,
                    sizeBytes = size,
                    dateTakenMs = date,
                    isVideo = video,
                )
                n++
            }
        }
        return result
    }
}
