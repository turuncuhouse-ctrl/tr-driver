package net.neciparmagan.trdriver.backup

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CancellationException
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.data.UploadedMediaDb

/**
 * Rolling remote integrity check for gallery backup.
 *
 * A local "uploaded" flag alone is not a backup guarantee: the file can be
 * deleted server-side, purged from trash, or lost with a restored database.
 * Once a day (on Wi-Fi) we probe a small window of remote ids. A confirmed
 * 404/410 removes the local record so the next scan re-uploads the item.
 *
 * Auth / network errors abort the run WITHOUT deleting anything, so a token
 * glitch or a temporary outage can never cause data loss.
 */
object BackupVerifier {
    private const val TAG = "BackupVerifier"
    private const val PREFS = "trdriver_backup_health"
    private const val KEY_LAST_RUN = "last_verify_at"
    private const val KEY_OFFSET = "verify_offset"
    private const val INTERVAL_MS = 24L * 60L * 60L * 1000L
    private const val WINDOW = 40
    private const val MAX_OFFSET = 100_000

    suspend fun verifyIfDue(context: Context) {
        val app = context.applicationContext
        val prefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val now = System.currentTimeMillis()
        if (now - prefs.getLong(KEY_LAST_RUN, 0L) < INTERVAL_MS) return

        val session = SessionStore(app)
        if (!session.isLoggedIn || !session.galleryBackupEnabled) return
        if (!UploadNetworkGate.isWifi(app)) return

        val db = UploadedMediaDb(app)
        val offset = prefs.getInt(KEY_OFFSET, 0).coerceIn(0, MAX_OFFSET)
        val rows = db.listForVerification(offset = offset, limit = WINDOW)
        if (rows.isEmpty()) {
            // Reached the end of the catalogue: start the next pass from the top.
            prefs.edit().putLong(KEY_LAST_RUN, now).putInt(KEY_OFFSET, 0).apply()
            return
        }

        val api = DriveApi(session, app)
        var checked = 0
        var missing = 0
        var aborted = false
        for (row in rows) {
            try {
                if (!api.remoteFileExists(row.remoteId)) {
                    db.forget(row.mediaKey)
                    missing++
                }
                checked++
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                // Never delete records on an inconclusive answer.
                Log.w(TAG, "verification aborted: ${e.message}")
                aborted = true
                break
            }
        }

        if (!aborted) {
            val nextOffset = if (checked < WINDOW) 0 else (offset + checked) % MAX_OFFSET
            prefs.edit()
                .putLong(KEY_LAST_RUN, now)
                .putInt(KEY_OFFSET, nextOffset)
                .apply()
        }

        if (missing > 0) {
            Log.i(TAG, "$missing remote file(s) missing — queued for re-upload")
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount + missing,
                message = "Sunucuda $missing dosya eksik bulundu; yeniden yedeklenecek",
                clearFileBytes = true,
            )
        }
    }
}
