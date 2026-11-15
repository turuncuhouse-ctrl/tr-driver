package net.neciparmagan.trdriver.data

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import android.net.Uri

data class UploadedMediaRow(
    val mediaKey: String,
    val remoteId: String,
    val sizeBytes: Long,
    val uploadedAt: Long,
    val localUri: String,
    val freedAt: Long?,
)

/** Catalogue snapshot used by incremental change detection. */
data class UploadedCatalogRow(
    val sizeBytes: Long,
    val mtimeMs: Long,
    val contentHash: String,
)

/** In-flight resumable (chunked) upload that can continue after a restart. */
data class PendingUploadRow(
    val mediaKey: String,
    val batchId: String,
    val sessionId: String,
    val parentId: String,
    val fileName: String,
    val expectedSize: Long,
    val mtimeMs: Long,
    val receivedBytes: Long,
)

/**
 * Tracks gallery items uploaded to the server.
 * Free-up only deletes local files after remote verification succeeds.
 *
 * Schema v3 adds incremental-backup metadata (mtime + content hash) and a
 * pending_uploads table so a chunked upload survives process death.
 */
class UploadedMediaDb(context: Context) :
    SQLiteOpenHelper(context.applicationContext, "gallery_backup.db", null, 3) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE IF NOT EXISTS uploaded_media(
              media_key TEXT PRIMARY KEY NOT NULL,
              remote_id TEXT NOT NULL,
              size_bytes INTEGER NOT NULL,
              uploaded_at INTEGER NOT NULL,
              local_uri TEXT NOT NULL DEFAULT '',
              freed_at INTEGER,
              mtime_ms INTEGER NOT NULL DEFAULT -1,
              content_hash TEXT NOT NULL DEFAULT ''
            )
            """.trimIndent(),
        )
        db.execSQL(
            """
            CREATE TABLE IF NOT EXISTS pending_uploads(
              media_key TEXT PRIMARY KEY NOT NULL,
              batch_id TEXT NOT NULL,
              session_id TEXT NOT NULL,
              parent_id TEXT NOT NULL DEFAULT '',
              file_name TEXT NOT NULL,
              expected_size INTEGER NOT NULL,
              mtime_ms INTEGER NOT NULL,
              received_bytes INTEGER NOT NULL DEFAULT 0,
              updated_at INTEGER NOT NULL
            )
            """.trimIndent(),
        )
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        if (oldVersion < 2) {
            db.execSQL("ALTER TABLE uploaded_media ADD COLUMN local_uri TEXT NOT NULL DEFAULT ''")
            db.execSQL("ALTER TABLE uploaded_media ADD COLUMN freed_at INTEGER")
        }
        if (oldVersion < 3) {
            db.execSQL("ALTER TABLE uploaded_media ADD COLUMN mtime_ms INTEGER NOT NULL DEFAULT -1")
            db.execSQL("ALTER TABLE uploaded_media ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''")
            db.execSQL(
                """
                CREATE TABLE IF NOT EXISTS pending_uploads(
                  media_key TEXT PRIMARY KEY NOT NULL,
                  batch_id TEXT NOT NULL,
                  session_id TEXT NOT NULL,
                  parent_id TEXT NOT NULL DEFAULT '',
                  file_name TEXT NOT NULL,
                  expected_size INTEGER NOT NULL,
                  mtime_ms INTEGER NOT NULL,
                  received_bytes INTEGER NOT NULL DEFAULT 0,
                  updated_at INTEGER NOT NULL
                )
                """.trimIndent(),
            )
        }
    }

    fun isUploaded(mediaKey: String): Boolean {
        readableDatabase.rawQuery(
            "SELECT 1 FROM uploaded_media WHERE media_key=? LIMIT 1",
            arrayOf(mediaKey),
        ).use { return it.moveToFirst() }
    }

    fun remoteIdFor(mediaKey: String): String? {
        if (mediaKey.isBlank()) return null
        readableDatabase.rawQuery(
            "SELECT remote_id FROM uploaded_media WHERE media_key=? AND remote_id <> '' LIMIT 1",
            arrayOf(mediaKey),
        ).use { c ->
            return if (c.moveToFirst()) c.getString(0)?.takeIf { it.isNotBlank() } else null
        }
    }

    fun allRemoteIds(): Map<String, String> {
        val out = HashMap<String, String>()
        readableDatabase.rawQuery(
            "SELECT media_key, remote_id FROM uploaded_media WHERE remote_id <> ''",
            null,
        ).use { c ->
            val iKey = c.getColumnIndex("media_key")
            val iRemote = c.getColumnIndex("remote_id")
            if (iKey < 0 || iRemote < 0) return emptyMap()
            while (c.moveToNext()) {
                val key = c.getString(iKey) ?: continue
                val remote = c.getString(iRemote) ?: continue
                if (key.isNotBlank() && remote.isNotBlank()) out[key] = remote
            }
        }
        return out
    }

    /** All media keys that have been backed up (for gallery cloud badges). */
    fun allUploadedKeys(): Set<String> {
        val out = HashSet<String>()
        readableDatabase.rawQuery("SELECT media_key FROM uploaded_media", null).use { c ->
            val idx = c.getColumnIndex("media_key")
            if (idx < 0) return emptySet()
            while (c.moveToNext()) {
                out += c.getString(idx)
            }
        }
        return out
    }

    /** One read for incremental decisions: key → (size, mtime, hash). */
    fun allCatalogRows(): Map<String, UploadedCatalogRow> {
        val out = HashMap<String, UploadedCatalogRow>()
        readableDatabase.rawQuery(
            "SELECT media_key, size_bytes, mtime_ms, content_hash FROM uploaded_media",
            null,
        ).use { c ->
            val iKey = c.getColumnIndex("media_key")
            val iSize = c.getColumnIndex("size_bytes")
            val iMtime = c.getColumnIndex("mtime_ms")
            val iHash = c.getColumnIndex("content_hash")
            if (iKey < 0 || iSize < 0) return emptyMap()
            while (c.moveToNext()) {
                val key = c.getString(iKey) ?: continue
                out[key] = UploadedCatalogRow(
                    sizeBytes = c.getLong(iSize),
                    mtimeMs = if (iMtime >= 0) c.getLong(iMtime) else -1L,
                    contentHash = if (iHash >= 0) c.getString(iHash) ?: "" else "",
                )
            }
        }
        return out
    }

    fun markUploaded(
        mediaKey: String,
        remoteId: String,
        sizeBytes: Long,
        localUri: Uri? = null,
        mtimeMs: Long = -1L,
        contentHash: String = "",
    ) {
        if (mediaKey.isBlank() || remoteId.isBlank()) return
        writableDatabase.execSQL(
            """
            INSERT OR REPLACE INTO uploaded_media(
              media_key, remote_id, size_bytes, uploaded_at, local_uri, freed_at, mtime_ms, content_hash
            ) VALUES(?,?,?,?,?,NULL,?,?)
            """.trimIndent(),
            arrayOf(
                mediaKey,
                remoteId,
                sizeBytes.coerceAtLeast(0L),
                System.currentTimeMillis(),
                localUri?.toString().orEmpty(),
                mtimeMs,
                contentHash,
            ),
        )
    }

    /**
     * Record mtime/hash without touching the remote id. A blank [contentHash]
     * keeps any previously stored hash (adopting metadata must not lose it).
     */
    fun recordMeta(mediaKey: String, mtimeMs: Long, contentHash: String = "") {
        if (mediaKey.isBlank()) return
        if (contentHash.isBlank()) {
            writableDatabase.execSQL(
                "UPDATE uploaded_media SET mtime_ms=?, content_hash=content_hash WHERE media_key=?",
                arrayOf(mtimeMs, mediaKey),
            )
        } else {
            writableDatabase.execSQL(
                "UPDATE uploaded_media SET mtime_ms=?, content_hash=? WHERE media_key=?",
                arrayOf(mtimeMs, contentHash.uppercase(), mediaKey),
            )
        }
    }

    fun markFreed(mediaKey: String) {
        writableDatabase.execSQL(
            "UPDATE uploaded_media SET freed_at=? WHERE media_key=?",
            arrayOf(System.currentTimeMillis(), mediaKey),
        )
    }

    fun markFreedMany(keys: Collection<String>) {
        if (keys.isEmpty()) return
        val now = System.currentTimeMillis()
        writableDatabase.beginTransaction()
        try {
            for (key in keys) {
                writableDatabase.execSQL(
                    "UPDATE uploaded_media SET freed_at=? WHERE media_key=?",
                    arrayOf(now, key),
                )
            }
            writableDatabase.setTransactionSuccessful()
        } finally {
            writableDatabase.endTransaction()
        }
    }

    fun countUploaded(): Int {
        readableDatabase.rawQuery("SELECT COUNT(*) FROM uploaded_media", null).use {
            return if (it.moveToFirst()) it.getInt(0) else 0
        }
    }

    fun countNotFreed(): Int {
        readableDatabase.rawQuery("SELECT COUNT(*) FROM uploaded_media WHERE freed_at IS NULL", null).use {
            return if (it.moveToFirst()) it.getInt(0) else 0
        }
    }

    fun sumNotFreedBytes(): Long {
        readableDatabase.rawQuery(
            "SELECT COALESCE(SUM(size_bytes),0) FROM uploaded_media WHERE freed_at IS NULL",
            null,
        ).use {
            return if (it.moveToFirst()) it.getLong(0) else 0L
        }
    }

    /**
     * Rolling window for remote integrity verification.
     * Returns rows that still have a remote id, oldest first, so repeated runs
     * eventually cover the whole catalogue without a huge burst of requests.
     */
    fun listForVerification(offset: Int = 0, limit: Int = 40): List<UploadedMediaRow> {
        val out = ArrayList<UploadedMediaRow>(limit)
        readableDatabase.rawQuery(
            """
            SELECT media_key, remote_id, size_bytes, uploaded_at, local_uri, freed_at
            FROM uploaded_media
            WHERE remote_id <> ''
            ORDER BY uploaded_at ASC
            LIMIT ? OFFSET ?
            """.trimIndent(),
            arrayOf(limit.toString(), offset.coerceAtLeast(0).toString()),
        ).use { c ->
            val iKey = c.getColumnIndexOrThrow("media_key")
            val iRemote = c.getColumnIndexOrThrow("remote_id")
            val iSize = c.getColumnIndexOrThrow("size_bytes")
            val iUp = c.getColumnIndexOrThrow("uploaded_at")
            val iUri = c.getColumnIndexOrThrow("local_uri")
            val iFreed = c.getColumnIndexOrThrow("freed_at")
            while (c.moveToNext()) {
                out += UploadedMediaRow(
                    mediaKey = c.getString(iKey),
                    remoteId = c.getString(iRemote),
                    sizeBytes = c.getLong(iSize),
                    uploadedAt = c.getLong(iUp),
                    localUri = c.getString(iUri) ?: "",
                    freedAt = if (c.isNull(iFreed)) null else c.getLong(iFreed),
                )
            }
        }
        return out
    }

    /** Forget a backup record whose remote copy is gone so it gets re-uploaded. */
    fun forget(mediaKey: String) {
        if (mediaKey.isBlank()) return
        writableDatabase.execSQL(
            "DELETE FROM uploaded_media WHERE media_key=?",
            arrayOf(mediaKey),
        )
    }

    /** Candidates still on device (not marked freed). */
    fun listFreeable(limit: Int = 2000): List<UploadedMediaRow> {
        val out = ArrayList<UploadedMediaRow>(limit)
        readableDatabase.rawQuery(
            """
            SELECT media_key, remote_id, size_bytes, uploaded_at, local_uri, freed_at
            FROM uploaded_media
            WHERE freed_at IS NULL AND remote_id <> ''
            ORDER BY uploaded_at ASC
            LIMIT ?
            """.trimIndent(),
            arrayOf(limit.toString()),
        ).use { c ->
            val iKey = c.getColumnIndexOrThrow("media_key")
            val iRemote = c.getColumnIndexOrThrow("remote_id")
            val iSize = c.getColumnIndexOrThrow("size_bytes")
            val iUp = c.getColumnIndexOrThrow("uploaded_at")
            val iUri = c.getColumnIndexOrThrow("local_uri")
            val iFreed = c.getColumnIndexOrThrow("freed_at")
            while (c.moveToNext()) {
                out += UploadedMediaRow(
                    mediaKey = c.getString(iKey),
                    remoteId = c.getString(iRemote),
                    sizeBytes = c.getLong(iSize),
                    uploadedAt = c.getLong(iUp),
                    localUri = c.getString(iUri) ?: "",
                    freedAt = if (c.isNull(iFreed)) null else c.getLong(iFreed),
                )
            }
        }
        return out
    }

    // --- resumable upload session persistence -------------------------------

    fun pendingUploadFor(mediaKey: String): PendingUploadRow? {
        if (mediaKey.isBlank()) return null
        readableDatabase.rawQuery(
            """
            SELECT media_key, batch_id, session_id, parent_id, file_name,
                   expected_size, mtime_ms, received_bytes
            FROM pending_uploads WHERE media_key=?
            """.trimIndent(),
            arrayOf(mediaKey),
        ).use { c ->
            if (!c.moveToFirst()) return null
            return PendingUploadRow(
                mediaKey = c.getString(0),
                batchId = c.getString(1),
                sessionId = c.getString(2),
                parentId = c.getString(3),
                fileName = c.getString(4),
                expectedSize = c.getLong(5),
                mtimeMs = c.getLong(6),
                receivedBytes = c.getLong(7),
            )
        }
    }

    fun savePendingUpload(row: PendingUploadRow) {
        if (row.mediaKey.isBlank() || row.sessionId.isBlank()) return
        val cv = ContentValues().apply {
            put("media_key", row.mediaKey)
            put("batch_id", row.batchId)
            put("session_id", row.sessionId)
            put("parent_id", row.parentId)
            put("file_name", row.fileName)
            put("expected_size", row.expectedSize)
            put("mtime_ms", row.mtimeMs)
            put("received_bytes", row.receivedBytes.coerceAtLeast(0L))
            put("updated_at", System.currentTimeMillis())
        }
        writableDatabase.insertWithOnConflict(
            "pending_uploads",
            null,
            cv,
            SQLiteDatabase.CONFLICT_REPLACE,
        )
    }

    fun updatePendingReceived(mediaKey: String, receivedBytes: Long) {
        if (mediaKey.isBlank()) return
        writableDatabase.execSQL(
            "UPDATE pending_uploads SET received_bytes=?, updated_at=? WHERE media_key=?",
            arrayOf(receivedBytes.coerceAtLeast(0L), System.currentTimeMillis(), mediaKey),
        )
    }

    fun clearPendingUpload(mediaKey: String) {
        if (mediaKey.isBlank()) return
        writableDatabase.delete("pending_uploads", "media_key=?", arrayOf(mediaKey))
    }
}
