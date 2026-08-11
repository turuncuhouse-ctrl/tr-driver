package net.neciparmagan.trdriver.data

import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper

class UploadedMediaDb(context: Context) :
    SQLiteOpenHelper(context.applicationContext, "gallery_backup.db", null, 1) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE IF NOT EXISTS uploaded_media(
              media_key TEXT PRIMARY KEY NOT NULL,
              remote_id TEXT NOT NULL,
              size_bytes INTEGER NOT NULL,
              uploaded_at INTEGER NOT NULL
            )
            """.trimIndent()
        )
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) = Unit

    fun isUploaded(mediaKey: String): Boolean {
        readableDatabase.rawQuery(
            "SELECT 1 FROM uploaded_media WHERE media_key=? LIMIT 1",
            arrayOf(mediaKey),
        ).use { return it.moveToFirst() }
    }

    fun markUploaded(mediaKey: String, remoteId: String, sizeBytes: Long) {
        writableDatabase.execSQL(
            "INSERT OR REPLACE INTO uploaded_media(media_key, remote_id, size_bytes, uploaded_at) VALUES(?,?,?,?)",
            arrayOf(mediaKey, remoteId, sizeBytes, System.currentTimeMillis()),
        )
    }

    fun countUploaded(): Int {
        readableDatabase.rawQuery("SELECT COUNT(*) FROM uploaded_media", null).use {
            return if (it.moveToFirst()) it.getInt(0) else 0
        }
    }
}
