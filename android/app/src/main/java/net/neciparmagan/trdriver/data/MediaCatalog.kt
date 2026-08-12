package net.neciparmagan.trdriver.data

import android.content.ContentUris
import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import androidx.documentfile.provider.DocumentFile
import java.util.Calendar

data class LocalMedia(
    val mediaKey: String,
    val uri: Uri,
    val displayName: String,
    val mimeType: String,
    val sizeBytes: Long,
    val dateTakenMs: Long,
    val isVideo: Boolean,
    /** Optional label for SAF tree backups (folder display name). */
    val backupFolderLabel: String? = null,
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

    fun scanDocumentTrees(context: Context, treeUris: List<String>, limitPerTree: Int = 300): List<LocalMedia> {
        val out = ArrayList<LocalMedia>()
        for (raw in treeUris) {
            val uri = runCatching { Uri.parse(raw) }.getOrNull() ?: continue
            val tree = DocumentFile.fromTreeUri(context, uri) ?: continue
            val label = tree.name?.ifBlank { null } ?: "Klasor"
            walkTree(tree, label, out, limitPerTree, depth = 0)
        }
        return out.sortedByDescending { it.dateTakenMs }
    }

    private fun walkTree(
        dir: DocumentFile,
        label: String,
        out: MutableList<LocalMedia>,
        limit: Int,
        depth: Int,
    ) {
        if (out.size >= limit || depth > 8) return
        val children = runCatching { dir.listFiles() }.getOrNull() ?: return
        for (f in children) {
            if (out.size >= limit) return
            when {
                f.isDirectory -> walkTree(f, label, out, limit, depth + 1)
                f.isFile -> {
                    val size = f.length()
                    if (size in 1 until 1024) continue
                    val name = f.name ?: continue
                    val mime = f.type ?: "application/octet-stream"
                    out += LocalMedia(
                        mediaKey = "saf:${f.uri}",
                        uri = f.uri,
                        displayName = name,
                        mimeType = mime,
                        sizeBytes = size,
                        dateTakenMs = f.lastModified().coerceAtLeast(0L),
                        isVideo = mime.startsWith("video/"),
                        backupFolderLabel = label,
                    )
                }
            }
        }
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
