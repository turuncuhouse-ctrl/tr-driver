package net.neciparmagan.trdriver.data

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper

/** Last sync summary for one kind, shown in the settings screen. */
data class PersonalBackupMeta(
    val account: String,
    val lastSyncAt: Long,
    val lastTotal: Int,
    val lastMessage: String,
    val lastOk: Boolean,
)

/**
 * Local sync state for contacts / SMS backup.
 *
 * Only hashes, revisions and item keys are stored — never a phone number, a
 * name or a message body. The server is the only copy of the actual data.
 */
class PersonalBackupDb(context: Context) :
    SQLiteOpenHelper(context.applicationContext, "personal_backup.db", null, 1) {

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """
            CREATE TABLE IF NOT EXISTS personal_items(
              kind TEXT NOT NULL,
              item_key TEXT NOT NULL,
              hash TEXT NOT NULL DEFAULT '',
              revision INTEGER NOT NULL DEFAULT 0,
              synced_at INTEGER NOT NULL DEFAULT 0,
              PRIMARY KEY(kind, item_key)
            )
            """.trimIndent(),
        )
        db.execSQL(
            """
            CREATE TABLE IF NOT EXISTS personal_meta(
              kind TEXT PRIMARY KEY NOT NULL,
              account TEXT NOT NULL DEFAULT '',
              last_sync_at INTEGER NOT NULL DEFAULT 0,
              last_total INTEGER NOT NULL DEFAULT 0,
              last_message TEXT NOT NULL DEFAULT '',
              last_ok INTEGER NOT NULL DEFAULT 0
            )
            """.trimIndent(),
        )
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        // v1 is the first schema; future migrations are additive.
    }

    fun storedFor(kind: String): Map<String, PersonalBackupPolicy.Stored> {
        val out = HashMap<String, PersonalBackupPolicy.Stored>()
        readableDatabase.rawQuery(
            "SELECT item_key, hash, revision FROM personal_items WHERE kind=?",
            arrayOf(kind),
        ).use { cursor ->
            while (cursor.moveToNext()) {
                val key = cursor.getString(0) ?: continue
                out[key] = PersonalBackupPolicy.Stored(
                    revision = cursor.getLong(2),
                    hash = cursor.getString(1).orEmpty(),
                )
            }
        }
        return out
    }

    fun markSynced(items: List<SyncedItem>, kind: String) {
        if (items.isEmpty()) return
        val db = writableDatabase
        db.beginTransaction()
        try {
            val values = ContentValues()
            for (item in items) {
                values.clear()
                values.put("kind", kind)
                values.put("item_key", item.key)
                values.put("hash", item.hash)
                values.put("revision", item.revision)
                values.put("synced_at", System.currentTimeMillis())
                db.insertWithOnConflict(
                    "personal_items",
                    null,
                    values,
                    SQLiteDatabase.CONFLICT_REPLACE,
                )
            }
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    fun removeSynced(kind: String, keys: List<String>) {
        if (keys.isEmpty()) return
        val db = writableDatabase
        db.beginTransaction()
        try {
            for (key in keys) {
                db.delete("personal_items", "kind=? AND item_key=?", arrayOf(kind, key))
            }
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    /** Forget everything about one kind (account switch, purge, permissions off). */
    fun clearKind(kind: String) {
        writableDatabase.delete("personal_items", "kind=?", arrayOf(kind))
        recordMeta(kind, account = "", total = 0, message = "", ok = false, syncedAt = 0L)
    }

    fun meta(kind: String): PersonalBackupMeta {
        readableDatabase.rawQuery(
            "SELECT account, last_sync_at, last_total, last_message, last_ok FROM personal_meta WHERE kind=?",
            arrayOf(kind),
        ).use { cursor ->
            if (cursor.moveToFirst()) {
                return PersonalBackupMeta(
                    account = cursor.getString(0).orEmpty(),
                    lastSyncAt = cursor.getLong(1),
                    lastTotal = cursor.getInt(2),
                    lastMessage = cursor.getString(3).orEmpty(),
                    lastOk = cursor.getInt(4) != 0,
                )
            }
        }
        return PersonalBackupMeta(account = "", lastSyncAt = 0L, lastTotal = 0, lastMessage = "", lastOk = false)
    }

    fun recordMeta(
        kind: String,
        account: String,
        total: Int,
        message: String,
        ok: Boolean,
        syncedAt: Long = System.currentTimeMillis(),
    ) {
        val values = ContentValues().apply {
            put("kind", kind)
            put("account", account)
            put("last_sync_at", syncedAt)
            put("last_total", total)
            put("last_message", message)
            put("last_ok", if (ok) 1 else 0)
        }
        writableDatabase.insertWithOnConflict(
            "personal_meta",
            null,
            values,
            SQLiteDatabase.CONFLICT_REPLACE,
        )
    }

    /**
     * Close the underlying connection once a batch or screen is done with it;
     * a leaked helper keeps a SQLite connection (and its memory) alive.
     */
    fun release() {
        runCatching { close() }
    }

    fun countStored(kind: String): Int {
        readableDatabase.rawQuery(
            "SELECT COUNT(*) FROM personal_items WHERE kind=?",
            arrayOf(kind),
        ).use { cursor -> return if (cursor.moveToFirst()) cursor.getInt(0) else 0 }
    }
}

/** One item the server confirmed, remembered for the next incremental pass. */
data class SyncedItem(
    val key: String,
    val hash: String,
    val revision: Long,
)
