package net.neciparmagan.trdriver.data

import kotlin.coroutines.resume
import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper

data class UploadQueueRow(
    val id: Long,
    val source: String,
    val parentId: String,
    val localUri: String,
    val displayName: String,
    val conflictPolicy: String,
    val state: String,
    val attempts: Int,
    val lastError: String?,
)

class UploadQueueDb(context: Context) : SQLiteOpenHelper(context.applicationContext, "upload_queue.db", null, 1) {
    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            create table upload_queue (
                id integer primary key autoincrement,
                source text not null,
                parent_id text not null,
                local_uri text not null,
                display_name text not null,
                conflict_policy text not null default 'ask',
                state text not null default 'pending',
                attempts integer not null default 0,
                last_error text,
                created_at integer not null
            )
            """.trimIndent(),
        )
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) = Unit

    fun enqueue(
        source: String,
        parentId: String,
        localUri: String,
        displayName: String,
        conflictPolicy: UploadConflictPolicy = UploadConflictPolicy.ASK,
    ): Long {
        val cv = ContentValues().apply {
            put("source", source)
            put("parent_id", parentId)
            put("local_uri", localUri)
            put("display_name", displayName)
            put("conflict_policy", conflictPolicy.name.lowercase())
            put("state", "pending")
            put("attempts", 0)
            put("created_at", System.currentTimeMillis())
        }
        return writableDatabase.insert("upload_queue", null, cv)
    }

    fun nextPending(): UploadQueueRow? {
        readableDatabase.query(
            "upload_queue",
            arrayOf("id", "source", "parent_id", "local_uri", "display_name", "conflict_policy", "state", "attempts", "last_error"),
            "state = ?",
            arrayOf("pending"),
            null,
            null,
            "created_at asc",
            "1",
        ).use { c ->
            if (!c.moveToFirst()) return null
            return UploadQueueRow(
                id = c.getLong(0),
                source = c.getString(1),
                parentId = c.getString(2),
                localUri = c.getString(3),
                displayName = c.getString(4),
                conflictPolicy = c.getString(5),
                state = c.getString(6),
                attempts = c.getInt(7),
                lastError = c.getString(8),
            )
        }
    }

    fun markDone(id: Long) {
        writableDatabase.delete("upload_queue", "id = ?", arrayOf(id.toString()))
    }

    fun markFailed(id: Long, error: String) {
        writableDatabase.execSQL(
            "update upload_queue set state='pending', attempts=attempts+1, last_error=? where id=?",
            arrayOf(error.take(500), id.toString()),
        )
    }

    fun countPending(): Int {
        readableDatabase.rawQuery("select count(*) from upload_queue where state='pending'", null).use { c ->
            return if (c.moveToFirst()) c.getInt(0) else 0
        }
    }

    fun clearAll() {
        writableDatabase.delete("upload_queue", null, null)
    }
}

/** Optional UI hook for duplicate-name prompts (intake / manual gallery). */
object UploadConflictUi {
    var askHandler: (suspend (fileName: String) -> UploadConflictPolicy)? = null

    fun bindActivity(activity: androidx.appcompat.app.AppCompatActivity) {
        askHandler = { fileName -> prompt(activity, fileName) }
    }

    fun unbindActivity() {
        askHandler = null
    }

    private suspend fun prompt(
        activity: androidx.appcompat.app.AppCompatActivity,
        fileName: String,
    ): UploadConflictPolicy = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.Main) {
        kotlinx.coroutines.suspendCancellableCoroutine { cont ->
            val dialog = androidx.appcompat.app.AlertDialog.Builder(activity)
                .setTitle("Dosya zaten var")
                .setMessage(
                    "\"$fileName\" bu klasörde mevcut.\n\n" +
                        "Üzerine yaz, yeniden adlandır veya atla.",
                )
                .setPositiveButton("Üzerine yaz") { _, _ ->
                    cont.resume(UploadConflictPolicy.OVERWRITE) {}
                }
                .setNeutralButton("Yeniden adlandır") { _, _ ->
                    cont.resume(UploadConflictPolicy.RENAME) {}
                }
                .setNegativeButton("Atla") { _, _ ->
                    cont.resume(UploadConflictPolicy.SKIP) {}
                }
                .setOnCancelListener {
                    cont.resume(UploadConflictPolicy.SKIP) {}
                }
                .create()
            cont.invokeOnCancellation { dialog.dismiss() }
            dialog.show()
        }
    }
}
