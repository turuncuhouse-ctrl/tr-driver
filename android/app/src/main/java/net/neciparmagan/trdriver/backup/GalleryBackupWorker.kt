package net.neciparmagan.trdriver.backup

import android.content.Context
import android.util.Log
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import kotlinx.coroutines.delay
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.MediaCatalog
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadedMediaDb
import java.util.concurrent.TimeUnit

class GalleryBackupWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val session = SessionStore(applicationContext)
        if (!session.isLoggedIn || !session.galleryBackupEnabled) {
            return Result.success()
        }
        val db = UploadedMediaDb(applicationContext)
        val api = DriveApi(session, applicationContext)
        return try {
            val media = MediaCatalog.scan(applicationContext, limit = 1000)
            var uploaded = 0
            var skipped = 0
            var pendingLeft = 0
            for (item in media) {
                if (isStopped) break
                if (db.isUploaded(item.mediaKey)) {
                    skipped++
                    continue
                }
                if (item.sizeBytes in 1 until 1024) {
                    skipped++
                    continue
                }
                // One file per run keeps the server and mobile radio calm.
                if (uploaded >= 1) {
                    pendingLeft++
                    continue
                }
                try {
                    session.lastBackupMessage = "Yedekleniyor: ${item.displayName}"
                    val parent = api.ensurePhotosAlbumFolder(item)
                    val entry = api.uploadMedia(parent, item)
                    db.markUploaded(item.mediaKey, entry.id, item.sizeBytes)
                    uploaded++
                    delay(1500)
                } catch (e: Exception) {
                    Log.w(TAG, "upload failed ${item.displayName}: ${e.message}")
                    session.lastBackupMessage = "Yedek hata (${item.displayName}): ${e.message}"
                    // Soft backoff; try again later.
                    scheduleContinue(applicationContext, delayMinutes = 2)
                    return Result.success()
                }
            }
            // Count remaining after first success
            if (uploaded == 1) {
                pendingLeft = media.count { !db.isUploaded(it.mediaKey) && it.sizeBytes !in 1 until 1024 }
            }
            session.lastBackupMessage = when {
                uploaded > 0 && pendingLeft > 0 ->
                    "Yedek OK (+1). Kalan ~$pendingLeft · cihaz: ${session.deviceName}"
                uploaded > 0 ->
                    "Yedek tamam (+1). Toplam işaretli: ${db.countUploaded()}"
                else ->
                    "Yedek: yeni öğe yok ($skipped zaten vardı)"
            }
            if (pendingLeft > 0 && session.galleryBackupEnabled) {
                scheduleContinue(applicationContext, delayMinutes = 1)
            }
            Result.success()
        } catch (e: Exception) {
            session.lastBackupMessage = "Yedek hata: ${e.message}"
            Log.e(TAG, "backup failed", e)
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "GalleryBackup"
        const val UNIQUE_PERIODIC = "trdriver_gallery_periodic"
        const val UNIQUE_ONCE = "trdriver_gallery_once"
        const val UNIQUE_CONTINUE = "trdriver_gallery_continue"

        fun schedule(context: Context) {
            val session = SessionStore(context)
            val wm = WorkManager.getInstance(context.applicationContext)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) {
                wm.cancelUniqueWork(UNIQUE_PERIODIC)
                wm.cancelUniqueWork(UNIQUE_ONCE)
                wm.cancelUniqueWork(UNIQUE_CONTINUE)
                return
            }
            val constraints = constraints(session)
            val periodic = PeriodicWorkRequestBuilder<GalleryBackupWorker>(30, TimeUnit.MINUTES)
                .setConstraints(constraints)
                .build()
            wm.enqueueUniquePeriodicWork(
                UNIQUE_PERIODIC,
                ExistingPeriodicWorkPolicy.UPDATE,
                periodic,
            )
            runNow(context)
        }

        fun runNow(context: Context) {
            val session = SessionStore(context)
            if (!session.isLoggedIn) return
            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints(session))
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(UNIQUE_ONCE, ExistingWorkPolicy.REPLACE, once)
        }

        fun scheduleContinue(context: Context, delayMinutes: Long = 1) {
            val session = SessionStore(context)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) return
            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints(session))
                .setInitialDelay(delayMinutes, TimeUnit.MINUTES)
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(UNIQUE_CONTINUE, ExistingWorkPolicy.REPLACE, once)
        }

        private fun constraints(session: SessionStore) = Constraints.Builder()
            .setRequiredNetworkType(
                if (session.wifiOnlyBackup) NetworkType.UNMETERED else NetworkType.CONNECTED
            )
            .setRequiresBatteryNotLow(true)
            .build()
    }
}
