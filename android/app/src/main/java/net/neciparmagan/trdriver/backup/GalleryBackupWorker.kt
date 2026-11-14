package net.neciparmagan.trdriver.backup

import android.content.Context
import android.os.Build
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import kotlinx.coroutines.CancellationException
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.widget.BackupStatusWidget

/**
 * Bounded WorkManager fallback.
 *
 * The persistent TrKeepAliveService does the continuous work. This worker only
 * runs when that service is not alive (process killed, or Android 12+ refused a
 * background FGS launch). It never starts a second foreground service from the
 * background — that used to throw ForegroundServiceStartNotAllowedException and
 * silently kill the backup chain.
 */
class GalleryBackupWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val app = applicationContext
        val session = SessionStore(app)
        if (!session.isLoggedIn || !session.anyBackupEnabled()) {
            session.clearBackupProgress()
            BackupStatusWidget.refreshAll(app)
            BackupController.ensure(app)
            return Result.success()
        }

        if (TrKeepAliveService.running) {
            // Someone else is uploading; just nudge the loop and re-arm triggers.
            BackupController.ensure(app)
            BackupController.requestKick()
            return Result.success()
        }

        // Older Android versions allow an FGS launch from a worker; re-arm the
        // persistent service there. On 12+ the OS refuses, so we do bounded work.
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) {
            TrKeepAliveService.startIfNeeded(app)
            if (TrKeepAliveService.running) {
                BackupController.requestKick()
                return Result.success()
            }
        }

        safeSetForeground("TR Driver yedekleme…")
        return try {
            if (session.galleryBackupEnabled) {
                val result = GalleryBackupEngine.runBatch(
                    context = app,
                    isStopped = { isStopped },
                )
                session.lastBackupHeartbeatAt = System.currentTimeMillis()
                if (result.scheduleContinue && session.galleryBackupEnabled) {
                    BackupController.scheduleContinue(app, result.continueDelaySec)
                }
            }
            Result.success()
        } catch (e: CancellationException) {
            if (session.anyBackupEnabled() && session.isLoggedIn) {
                BackupController.scheduleContinue(app, 5L)
            }
            throw e
        } catch (e: Exception) {
            Log.e(TAG, "fallback backup failed", e)
            Result.retry()
        }
    }

    private suspend fun safeSetForeground(text: String) {
        try {
            setForeground(BackupNotifications.workerForegroundInfo(applicationContext, text))
        } catch (e: Exception) {
            Log.w(TAG, "setForeground skipped: ${e.message}")
        }
    }

    companion object {
        private const val TAG = "GalleryBackup"

        /** Called from Activities / the backup switch: safe to start the FGS. */
        fun schedule(context: Context) =
            BackupController.ensure(context, startForegroundService = true)

        /** User tapped "Yedekle". */
        fun runNow(context: Context) = BackupController.runNow(context)

        /** Kept for compatibility with existing call sites. */
        fun scheduleContinue(context: Context, delaySeconds: Long = 1L) =
            BackupController.scheduleContinue(context, delaySeconds)
    }
}
