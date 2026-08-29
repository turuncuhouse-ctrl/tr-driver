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
    val albumName: String? = null,
    val albumId: String? = null,
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

data class MediaAlbum(
    val id: String,
    val name: String,
    val count: Int,
    val coverUri: Uri?,
    val isVideoHeavy: Boolean = false,
)

object MediaCatalog {
    fun scan(context: Context, limit: Int = 2000): List<LocalMedia> {
        val perCollection = limit.coerceAtLeast(200)
        val out = ArrayList<LocalMedia>()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            for (vol in MediaStore.getExternalVolumeNames(context)) {
                out += query(context, MediaStore.Images.Media.getContentUri(vol), false, perCollection)
                out += query(context, MediaStore.Video.Media.getContentUri(vol), true, perCollection)
            }
        } else {
            out += query(context, MediaStore.Images.Media.EXTERNAL_CONTENT_URI, false, perCollection)
            out += query(context, MediaStore.Video.Media.EXTERNAL_CONTENT_URI, true, perCollection)
        }
        return out.distinctBy { it.mediaKey }
            .sortedByDescending { it.dateTakenMs }
            .take(limit.coerceAtLeast(perCollection))
    }

    fun scanAlbum(context: Context, albumId: String, limit: Int = 2000): List<LocalMedia> {
        return scan(context, limit).filter { it.albumId == albumId }
            .sortedByDescending { it.dateTakenMs }
    }

    /** Device albums like Camera, Screenshots, WhatsApp Images — like system Gallery. */
    fun listAlbums(context: Context, limitPerQuery: Int = 3000): List<MediaAlbum> {
        val media = scan(context, limitPerQuery)
        val grouped = linkedMapOf<String, MutableList<LocalMedia>>()
        for (item in media) {
            val key = item.albumId ?: "unknown"
            grouped.getOrPut(key) { ArrayList() }.add(item)
        }
        return grouped.map { (id, items) ->
            val sorted = items.sortedByDescending { it.dateTakenMs }
            val name = sorted.firstOrNull()?.albumName?.ifBlank { null }
                ?: if (id == "unknown") "Diğer" else id
            MediaAlbum(
                id = id,
                name = name,
                count = sorted.size,
                coverUri = sorted.firstOrNull()?.uri,
                isVideoHeavy = sorted.count { it.isVideo } > sorted.size / 2,
            )
        }.sortedByDescending { it.count }
    }

    fun scanDocumentTrees(context: Context, treeUris: List<String>, limitPerTree: Int = 300): List<LocalMedia> {
        val out = ArrayList<LocalMedia>()
        for (raw in treeUris) {
            val uri = runCatching { Uri.parse(raw) }.getOrNull() ?: continue
            val tree = DocumentFile.fromTreeUri(context, uri) ?: continue
            val label = tree.name?.ifBlank { null } ?: "Klasor"
            val treeItems = ArrayList<LocalMedia>()
            walkTree(tree, label, treeItems, limitPerTree, depth = 0)
            out += treeItems
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
                        albumName = label,
                        albumId = "saf:$label",
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
        val bucketIdCol = MediaStore.MediaColumns.BUCKET_ID
        val bucketNameCol = MediaStore.MediaColumns.BUCKET_DISPLAY_NAME
        val takenCol = MediaStore.MediaColumns.DATE_TAKEN
        val modifiedCol = MediaStore.MediaColumns.DATE_MODIFIED
        val addedCol = MediaStore.MediaColumns.DATE_ADDED
        val projection = arrayOf(
            idCol, nameCol, mimeCol, sizeCol,
            takenCol, modifiedCol, addedCol,
            bucketIdCol, bucketNameCol,
        )
        val selection = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            "${MediaStore.MediaColumns.IS_PENDING}=0"
        } else {
            null
        }
        // Prefer taken, then modified/added for stable ordering across OEMs.
        val sort = "$takenCol DESC, $modifiedCol DESC, $addedCol DESC"
        val result = ArrayList<LocalMedia>()
        runCatching {
            context.contentResolver.query(collection, projection, selection, null, sort)?.use { c ->
                val iId = c.getColumnIndexOrThrow(idCol)
                val iName = c.getColumnIndexOrThrow(nameCol)
                val iMime = c.getColumnIndexOrThrow(mimeCol)
                val iSize = c.getColumnIndexOrThrow(sizeCol)
                val iTaken = c.getColumnIndex(takenCol)
                val iModified = c.getColumnIndex(modifiedCol)
                val iAdded = c.getColumnIndex(addedCol)
                val iBucketId = c.getColumnIndex(bucketIdCol)
                val iBucketName = c.getColumnIndex(bucketNameCol)
                var n = 0
                while (c.moveToNext() && n < limit) {
                    val id = c.getLong(iId)
                    val name = c.getString(iName) ?: if (video) "video_$id.mp4" else "image_$id.jpg"
                    val mime = c.getString(iMime)
                        ?: if (video) "video/mp4" else "image/jpeg"
                    val size = c.getLong(iSize)
                    val date = resolveDateMs(
                        taken = if (iTaken >= 0) c.getLong(iTaken) else 0L,
                        modified = if (iModified >= 0) c.getLong(iModified) else 0L,
                        added = if (iAdded >= 0) c.getLong(iAdded) else 0L,
                    )
                    val uri = ContentUris.withAppendedId(collection, id)
                    val volume = collection.toString()
                    val bucketId = if (iBucketId >= 0) c.getString(iBucketId) else null
                    val bucketName = if (iBucketName >= 0) c.getString(iBucketName) else null
                    result += LocalMedia(
                        mediaKey = "$volume/$id",
                        uri = uri,
                        displayName = name,
                        mimeType = mime,
                        sizeBytes = size,
                        dateTakenMs = date,
                        isVideo = video,
                        albumName = bucketName,
                        albumId = bucketId,
                    )
                    n++
                }
            }
        }
        return result
    }

    /** Milliseconds since epoch; never invent "now" (that dumps old files into today's folder). */
    private fun resolveDateMs(taken: Long, modified: Long, added: Long): Long {
        fun normalize(raw: Long): Long {
            if (raw <= 0L) return 0L
            return if (raw < 10_000_000_000L) raw * 1000L else raw
        }
        normalize(taken).takeIf { it > 0L }?.let { return it }
        normalize(modified).takeIf { it > 0L }?.let { return it }
        normalize(added).takeIf { it > 0L }?.let { return it }
        // Last resort so undated items still land in a usable year/month folder.
        return System.currentTimeMillis()
    }
}
