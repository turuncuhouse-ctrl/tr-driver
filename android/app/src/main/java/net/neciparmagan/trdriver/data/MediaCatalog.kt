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
    /**
     * Last modification time used by incremental change detection (0 = unknown,
     * callers fall back to [dateTakenMs]). Kept separate from the "taken" date
     * so editing a photo's modification time never moves its album placement.
     */
    val modifiedMs: Long = 0L,
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
    /** Cloud album cover file id (download URL built at bind time). */
    val coverRemoteId: String? = null,
)

object MediaCatalog {
    /** Safety guard against pathological trees, not a backup policy cap. */
    private const val SAF_HARD_LIMIT = 100_000
    private const val SAF_MAX_DEPTH = 20

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

    /**
     * Full MediaStore scan for backup — deliberately unbounded.
     * The UI helpers above keep their limits; backup must never silently skip
     * older media on a large library (the old 4000 cap did exactly that).
     */
    fun scanAll(context: Context): List<LocalMedia> = scan(context, Int.MAX_VALUE)

    /**
     * Full MediaStore audio scan (music, recordings, WhatsApp audio visible
     * to MediaStore). Empty when READ_MEDIA_AUDIO is not granted.
     */
    fun scanAllAudio(context: Context): List<LocalMedia> {
        val out = ArrayList<LocalMedia>()
        runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                for (vol in MediaStore.getExternalVolumeNames(context)) {
                    out += query(
                        context,
                        MediaStore.Audio.Media.getContentUri(vol),
                        video = false,
                        limit = Int.MAX_VALUE,
                        fallbackMime = "audio/mpeg",
                        fallbackPrefix = "audio",
                        // Route to TR Backup / {device} / {topFolder} instead of
                        // photo month folders, and match preset-walk labels so
                        // the same file is never uploaded from both sources.
                        labelFromRelativePath = true,
                    )
                }
            } else {
                out += query(
                    context,
                    MediaStore.Audio.Media.EXTERNAL_CONTENT_URI,
                    video = false,
                    limit = Int.MAX_VALUE,
                    fallbackMime = "audio/mpeg",
                    fallbackPrefix = "audio",
                    labelFromRelativePath = true,
                )
            }
        }
        return out.distinctBy { it.mediaKey }.sortedByDescending { it.dateTakenMs }
    }

    /**
     * Best-effort scan of the Downloads collection (documents indexed by the
     * system). Often empty on Android 13+ without all-files access — SAF
     * preset folders and direct path walks cover that case instead.
     */
    fun scanDownloads(context: Context): List<LocalMedia> {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) return emptyList()
        val out = ArrayList<LocalMedia>()
        runCatching {
            for (vol in MediaStore.getExternalVolumeNames(context)) {
                out += query(
                    context,
                    MediaStore.Downloads.getContentUri(vol),
                    video = false,
                    limit = SAF_HARD_LIMIT,
                    fallbackMime = "application/octet-stream",
                    fallbackPrefix = "file",
                )
            }
        }
        return out.distinctBy { it.mediaKey }
            .filter { BackupPolicy.kindOf(it.mimeType, it.displayName) == BackupPolicy.KIND_DOC }
            // Same label as the Download preset folder so an item seen both via
            // MediaStore and via a direct/SAF walk is deduplicated, not
            // uploaded twice.
            .map { it.copy(backupFolderLabel = "Download") }
            .sortedByDescending { it.dateTakenMs }
    }

    /**
     * Direct java.io walk of preset folders (all files, any extension).
     * Only called when the app holds all-files access; every path is
     * validated by [BackupPolicy.isSafeDirectPath] first.
     */
    fun scanDirectPaths(context: Context, paths: List<String>): List<LocalMedia> {
        val out = ArrayList<LocalMedia>()
        for (raw in paths) {
            if (!BackupPolicy.isSafeDirectPath(raw)) continue
            val root = java.io.File(raw)
            if (!root.isDirectory) continue
            val label = root.name.ifBlank { "Klasor" }
            val items = ArrayList<LocalMedia>()
            walkFileTree(root, label, items, SAF_HARD_LIMIT, depth = 0)
            out += items
        }
        return out.sortedByDescending { it.dateTakenMs }
    }

    private fun walkFileTree(
        dir: java.io.File,
        label: String,
        out: MutableList<LocalMedia>,
        limit: Int,
        depth: Int,
    ) {
        if (out.size >= limit || depth > SAF_MAX_DEPTH) return
        val children = dir.listFiles() ?: return
        for (f in children) {
            if (out.size >= limit) return
            val name = f.name
            // Skip hidden entries (.thumbnails, temp files); ".nomedia" itself
            // is a dot-file so it is skipped while the folder is still walked —
            // explicit user-chosen folders must back up even if apps hide them.
            if (name.startsWith(".")) continue
            when {
                f.isDirectory -> walkFileTree(f, label, out, limit, depth + 1)
                f.isFile -> {
                    val size = f.length()
                    if (size <= 0L) continue
                    val ext = name.substringAfterLast('.', "").lowercase()
                    val mime = runCatching {
                        android.webkit.MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext)
                    }.getOrNull()
                        ?: MimeHelper.guessFromExtension(ext)
                        ?: "application/octet-stream"
                    val modified = f.lastModified().coerceAtLeast(0L)
                    out += LocalMedia(
                        mediaKey = "path:${f.absolutePath}",
                        uri = Uri.fromFile(f),
                        displayName = name,
                        mimeType = mime,
                        sizeBytes = size,
                        dateTakenMs = modified,
                        isVideo = mime.startsWith("video/"),
                        albumName = label,
                        albumId = "path:$label",
                        backupFolderLabel = label,
                        modifiedMs = modified,
                    )
                }
            }
        }
    }

    /** Full recursive SAF folder scan for backup (see scanAll). */
    fun scanAllDocumentTrees(context: Context, treeUris: List<String>): List<LocalMedia> =
        scanDocumentTrees(context, treeUris, limitPerTree = SAF_HARD_LIMIT)

    fun scanAlbum(context: Context, albumId: String, limit: Int = 2000): List<LocalMedia> {
        if (albumId.startsWith("saf:")) {
            return scan(context, limit).filter { it.albumId == albumId }
                .sortedByDescending { it.dateTakenMs }
        }
        val perCollection = limit.coerceAtLeast(200)
        val out = ArrayList<LocalMedia>()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            for (vol in MediaStore.getExternalVolumeNames(context)) {
                out += query(context, MediaStore.Images.Media.getContentUri(vol), false, perCollection, albumId)
                out += query(context, MediaStore.Video.Media.getContentUri(vol), true, perCollection, albumId)
            }
        } else {
            out += query(context, MediaStore.Images.Media.EXTERNAL_CONTENT_URI, false, perCollection, albumId)
            out += query(context, MediaStore.Video.Media.EXTERNAL_CONTENT_URI, true, perCollection, albumId)
        }
        return out.distinctBy { it.mediaKey }
            .sortedByDescending { it.dateTakenMs }
            .take(limit.coerceAtLeast(perCollection))
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
        if (out.size >= limit || depth > SAF_MAX_DEPTH) return
        val children = runCatching { dir.listFiles() }.getOrNull() ?: return
        for (f in children) {
            if (out.size >= limit) return
            when {
                f.isDirectory -> walkTree(f, label, out, limit, depth + 1)
                f.isFile -> {
                    val size = f.length()
                    // Back up small files too (notes, exports, configs); only skip empties.
                    if (size <= 0L) continue
                    val name = f.name ?: continue
                    val mime = f.type ?: "application/octet-stream"
                    val modified = f.lastModified().coerceAtLeast(0L)
                    out += LocalMedia(
                        mediaKey = "saf:${f.uri}",
                        uri = f.uri,
                        displayName = name,
                        mimeType = mime,
                        sizeBytes = size,
                        dateTakenMs = modified,
                        isVideo = mime.startsWith("video/"),
                        albumName = label,
                        albumId = "saf:$label",
                        backupFolderLabel = label,
                        modifiedMs = modified,
                    )
                }
            }
        }
    }

    private fun query(
        context: Context,
        collection: Uri,
        video: Boolean,
        limit: Int,
        bucketId: String? = null,
        fallbackMime: String? = null,
        fallbackPrefix: String? = null,
        labelFromRelativePath: Boolean = false,
    ): List<LocalMedia> {
        val idCol = MediaStore.MediaColumns._ID
        val nameCol = MediaStore.MediaColumns.DISPLAY_NAME
        val mimeCol = MediaStore.MediaColumns.MIME_TYPE
        val sizeCol = MediaStore.MediaColumns.SIZE
        val bucketIdCol = MediaStore.MediaColumns.BUCKET_ID
        val bucketNameCol = MediaStore.MediaColumns.BUCKET_DISPLAY_NAME
        val takenCol = MediaStore.MediaColumns.DATE_TAKEN
        val modifiedCol = MediaStore.MediaColumns.DATE_MODIFIED
        val addedCol = MediaStore.MediaColumns.DATE_ADDED
        // RELATIVE_PATH exists on Q+ only — never project it on older devices.
        val relPathCol = MediaStore.MediaColumns.RELATIVE_PATH
        val projection = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            arrayOf(
                idCol, nameCol, mimeCol, sizeCol,
                takenCol, modifiedCol, addedCol,
                bucketIdCol, bucketNameCol, relPathCol,
            )
        } else {
            arrayOf(
                idCol, nameCol, mimeCol, sizeCol,
                takenCol, modifiedCol, addedCol,
                bucketIdCol, bucketNameCol,
            )
        }
        val selection = buildString {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                append("${MediaStore.MediaColumns.IS_PENDING}=0")
            }
            if (bucketId != null) {
                if (isNotEmpty()) append(" AND ")
                append("$bucketIdCol=?")
            }
        }.ifBlank { null }
        val selectionArgs = if (bucketId != null) arrayOf(bucketId) else null
        // Prefer taken, then modified/added for stable ordering across OEMs.
        val sort = "$takenCol DESC, $modifiedCol DESC, $addedCol DESC"
        val result = ArrayList<LocalMedia>()
        runCatching {
            context.contentResolver.query(collection, projection, selection, selectionArgs, sort)?.use { c ->
                val iId = c.getColumnIndexOrThrow(idCol)
                val iName = c.getColumnIndexOrThrow(nameCol)
                val iMime = c.getColumnIndexOrThrow(mimeCol)
                val iSize = c.getColumnIndexOrThrow(sizeCol)
                val iTaken = c.getColumnIndex(takenCol)
                val iModified = c.getColumnIndex(modifiedCol)
                val iAdded = c.getColumnIndex(addedCol)
                val iBucketId = c.getColumnIndex(bucketIdCol)
                val iBucketName = c.getColumnIndex(bucketNameCol)
                val iRelPath = c.getColumnIndex(relPathCol)
                var n = 0
                while (c.moveToNext() && n < limit) {
                    val id = c.getLong(iId)
                    val name = c.getString(iName)
                        ?: "${fallbackPrefix ?: if (video) "video" else "image"}_$id"
                    val mime = c.getString(iMime)
                        ?: fallbackMime
                        ?: if (video) "video/mp4" else "image/jpeg"
                    val size = c.getLong(iSize)
                    val date = resolveDateMs(
                        taken = if (iTaken >= 0) c.getLong(iTaken) else 0L,
                        modified = if (iModified >= 0) c.getLong(iModified) else 0L,
                        added = if (iAdded >= 0) c.getLong(iAdded) else 0L,
                    )
                    val modifiedRaw = if (iModified >= 0) normalizeEpoch(c.getLong(iModified)) else 0L
                    val uri = ContentUris.withAppendedId(collection, id)
                    val volume = collection.toString()
                    val bucketId = if (iBucketId >= 0) c.getString(iBucketId) else null
                    val bucketName = if (iBucketName >= 0) c.getString(iBucketName) else null
                    val relLabel = if (labelFromRelativePath && iRelPath >= 0) {
                        topFolderLabel(c.getString(iRelPath))
                    } else {
                        null
                    }
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
                        backupFolderLabel = relLabel,
                        modifiedMs = modifiedRaw,
                    )
                    n++
                }
            }
        }
        return result
    }

    /**
     * First segment of a MediaStore relative path ("Music/Albums/x.mp3" →
     * "Music") so audio lands in a stable TR Backup subfolder that matches
     * preset-walk labels; files at the storage root fall back to "Ses".
     */
    private fun topFolderLabel(relativePath: String?): String {
        val trimmed = relativePath?.trim()?.trim('/')
        if (trimmed.isNullOrEmpty()) return "Ses"
        return trimmed.substringBefore('/').ifBlank { "Ses" }
    }

    /** Milliseconds since epoch; never invent "now" (that dumps old files into today's folder). */
    private fun resolveDateMs(taken: Long, modified: Long, added: Long): Long {
        normalizeEpoch(taken).takeIf { it > 0L }?.let { return it }
        normalizeEpoch(modified).takeIf { it > 0L }?.let { return it }
        normalizeEpoch(added).takeIf { it > 0L }?.let { return it }
        // Last resort so undated items still land in a usable year/month folder.
        return System.currentTimeMillis()
    }

    /** Seconds-vs-milliseconds normaliser; 0 stays 0 ("unknown"). */
    private fun normalizeEpoch(raw: Long): Long {
        if (raw <= 0L) return 0L
        return if (raw < 10_000_000_000L) raw * 1000L else raw
    }
}
