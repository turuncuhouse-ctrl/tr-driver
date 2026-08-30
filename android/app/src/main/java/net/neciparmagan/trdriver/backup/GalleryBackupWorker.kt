package net.neciparmagan.trdriver.backup

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.util.Log
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.OutOfQuotaPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import kotlinx.coroutines.CancellationException
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.widget.BackupStatusWidget
import java.util.concurrent.TimeUnit

class GalleryBackupWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val session = SessionStore(applicationContext)
        if (!session.isLoggedIn || !session.galleryBackupEnabled) {
            session.clearBackupProgress()
            BackupStatusWidget.refreshAll(applicationContext)
            return Result.success()
        }
        if (!session.backupOnWifi && !session.backupOnMobile) {
            return Result.success()
        }

        // Foreground service path (when allowed from background).
        if (GalleryBackupForegroundService.running) {
            return Result.success()
        }
        if (GalleryBackupForegroundService.tryStart(applicationContext)) {
            return Result.success()
        }

        // Fallback: run inside worker with typed foreground notification (Android 10+).
        safeSetForeground("Galeri yedekleme…")
        return try {
            val result = GalleryBackupEngine.runBatch(
                context = applicationContext,
                isStopped = { isStopped },
            )
            if (result.scheduleContinue && session.galleryBackupEnabled) {
                scheduleContinue(applicationContext, result.continueDelaySec)
            }
            Result.success()
        } catch (e: CancellationException) {
            if (session.galleryBackupEnabled && session.isLoggedIn) {
                scheduleContinue(applicationContext, continueDelaySeconds(applicationContext, session))
            }
            throw e
        } catch (e: Exception) {
            Log.e(TAG, "backup failed", e)
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
        const val UNIQUE_PERIODIC = "trdriver_gallery_periodic"
        const val UNIQUE_ONCE = "trdriver_gallery_once"
        const val UNIQUE_CONTINUE = "trdriver_gallery_continue"

        private fun continueDelaySeconds(context: Context, session: SessionStore): Long {
            if (session.backupOnMobile) return 2L
            val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
                ?: return 1L
            val network = cm.activeNetwork ?: return 1L
            val caps = cm.getNetworkCapabilities(network) ?: return 1L
            return if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) 1L else 2L
        }

        fun schedule(context: Context) {
            val session = SessionStore(context)
            val wm = WorkManager.getInstance(context.applicationContext)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) {
                wm.cancelUniqueWork(UNIQUE_PERIODIC)
                wm.cancelUniqueWork(UNIQUE_ONCE)
                wm.cancelUniqueWork(UNIQUE_CONTINUE)
                session.clearBackupProgress(message = session.lastBackupMessage.ifBlank { "Yedek kapalı" })
                BackupStatusWidget.refreshAll(context)
                return
            }
            if (!session.backupOnWifi && !session.backupOnMobile) return
            val constraints = constraints(session)
            val periodic = PeriodicWorkRequestBuilder<GalleryBackupWorker>(30, TimeUnit.MINUTES)
                .setConstraints(constraints)
                .build()
            wm.enqueueUniquePeriodicWork(
                UNIQUE_PERIODIC,
                ExistingPeriodicWorkPolicy.UPDATE,
                periodic,
            )
            enqueueOnce(context, ExistingWorkPolicy.KEEP)
        }

        /** User tapped "Yedekle" — start foreground service immediately (safe on all API levels). */
        fun runNow(context: Context) {
            val session = SessionStore(context)
            if (!session.isLoggedIn) return
            if (!session.galleryBackupEnabled) {
                session.galleryBackupEnabled = true
            }
            ensureNetwork(session)
            session.updateBackupProgress(
                active = true,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount.coerceAtLeast(1),
                message = "Yedek başlatılıyor…",
            )
            BackupStatusWidget.refreshAll(context)
            try {
                GalleryBackupForegroundService.start(context)
            } catch (e: Exception) {
                Log.w(TAG, "FGS start failed, using WorkManager", e)
                enqueueOnce(context, ExistingWorkPolicy.REPLACE)
            }
        }

        fun scheduleContinue(context: Context, delaySeconds: Long = 1) {
            val session = SessionStore(context)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) return
            if (!session.backupOnWifi && !session.backupOnMobile) return
            val builder = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints(session))
                .setInitialDelay(delaySeconds.coerceAtLeast(0), TimeUnit.SECONDS)
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.S) {
                builder.setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
            }
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(UNIQUE_CONTINUE, ExistingWorkPolicy.REPLACE, builder.build())
        }

        private fun enqueueOnce(context: Context, policy: ExistingWorkPolicy) {
            val session = SessionStore(context)
            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints(session))
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(UNIQUE_ONCE, policy, once)
        }

        private fun constraints(session: SessionStore) = Constraints.Builder()
            .setRequiredNetworkType(UploadNetworkGate.workManagerNetworkType(session))
            .build()

        private fun ensureNetwork(session: SessionStore) {
            if (!session.backupOnWifi && !session.backupOnMobile) {
                session.backupOnWifi = true
                session.backupOnMobile = true
            }
        }
    }
}
