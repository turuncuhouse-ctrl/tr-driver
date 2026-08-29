package net.neciparmagan.trdriver.data

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

/**
 * Tracks gallery items uploaded to the server.
 * Free-up only deletes local files after remote verification succeeds.
 */
class UploadedMediaDb(context: Context) :
    SQLiteOpenHelper(context.applicationContext, "gallery_backup.db", null, 2) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE IF NOT EXISTS uploaded_media(
              media_key TEXT PRIMARY KEY NOT NULL,
              remote_id TEXT NOT NULL,
              size_bytes INTEGER NOT NULL,
              uploaded_at INTEGER NOT NULL,
              local_uri TEXT NOT NULL DEFAULT '',
              freed_at INTEGER
            )
            """.trimIndent(),
        )
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        if (oldVersion < 2) {
            db.execSQL("ALTER TABLE uploaded_media ADD COLUMN local_uri TEXT NOT NULL DEFAULT ''")
            db.execSQL("ALTER TABLE uploaded_media ADD COLUMN freed_at INTEGER")
        }
    }

    fun isUploaded(mediaKey: String): Boolean {
        readableDatabase.rawQuery(
            "SELECT 1 FROM uploaded_media WHERE media_key=? LIMIT 1",
            arrayOf(mediaKey),
        ).use { return it.moveToFirst() }
    }

    fun markUploaded(mediaKey: String, remoteId: String, sizeBytes: Long, localUri: Uri? = null) {
        writableDatabase.execSQL(
            """
            INSERT OR REPLACE INTO uploaded_media(media_key, remote_id, size_bytes, uploaded_at, local_uri, freed_at)
            VALUES(?,?,?,?,?,NULL)
            """.trimIndent(),
            arrayOf(
                mediaKey,
                remoteId,
                sizeBytes,
                System.currentTimeMillis(),
                localUri?.toString().orEmpty(),
            ),
        )
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
        readableDatabase.rawQuery(
            "SELECT COUNT(*) FROM uploaded_media WHERE freed_at IS NULL",
            null,
        ).use { return if (it.moveToFirst()) it.getInt(0) else 0 }
    }

    fun sumNotFreedBytes(): Long {
        readableDatabase.rawQuery(
            "SELECT COALESCE(SUM(size_bytes),0) FROM uploaded_media WHERE freed_at IS NULL",
            null,
        ).use { return if (it.moveToFirst()) it.getLong(0) else 0L }
    }

    /** Candidates still on device (not marked freed). */
    fun listFreeable(limit: Int = 2000): List<UploadedMediaRow> {
        val out = ArrayList<UploadedMediaRow>()
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
}
