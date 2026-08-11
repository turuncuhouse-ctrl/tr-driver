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
            val media = MediaCatalog.scan(applicationContext, limit = 800)
            var uploaded = 0
            var skipped = 0
            var failed = 0
            for (item in media) {
                if (isStopped) break
                if (db.isUploaded(item.mediaKey)) {
                    skipped++
                    continue
                }
                // Skip tiny/corrupt leftovers
                if (item.sizeBytes in 1 until 1024) {
                    skipped++
                    continue
                }
                try {
                    val parent = api.ensurePhotosAlbumFolder(item)
                    val entry = api.uploadMedia(parent, item)
                    db.markUploaded(item.mediaKey, entry.id, item.sizeBytes)
                    uploaded++
                    // Keep each run bounded for battery/network friendliness.
                    if (uploaded >= 25) break
                } catch (e: Exception) {
                    failed++
                    Log.w(TAG, "upload failed ${item.displayName}: ${e.message}")
                    if (failed >= 5) break
                }
            }
            session.lastBackupMessage =
                "Yedek: +$uploaded yüklendi, $skipped zaten vardı" +
                    if (failed > 0) ", $failed hata" else ""
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

        fun schedule(context: Context) {
            val session = SessionStore(context)
            val wm = WorkManager.getInstance(context.applicationContext)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) {
                wm.cancelUniqueWork(UNIQUE_PERIODIC)
                wm.cancelUniqueWork(UNIQUE_ONCE)
                return
            }
            val constraints = Constraints.Builder()
                .setRequiredNetworkType(
                    if (session.wifiOnlyBackup) NetworkType.UNMETERED else NetworkType.CONNECTED
                )
                .setRequiresBatteryNotLow(true)
                .build()

            val periodic = PeriodicWorkRequestBuilder<GalleryBackupWorker>(15, TimeUnit.MINUTES)
                .setConstraints(constraints)
                .build()
            wm.enqueueUniquePeriodicWork(
                UNIQUE_PERIODIC,
                ExistingPeriodicWorkPolicy.UPDATE,
                periodic,
            )

            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints)
                .build()
            wm.enqueueUniqueWork(UNIQUE_ONCE, ExistingWorkPolicy.REPLACE, once)
        }

        fun runNow(context: Context) {
            val session = SessionStore(context)
            if (!session.isLoggedIn) return
            val constraints = Constraints.Builder()
                .setRequiredNetworkType(
                    if (session.wifiOnlyBackup) NetworkType.UNMETERED else NetworkType.CONNECTED
                )
                .build()
            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints)
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(UNIQUE_ONCE, ExistingWorkPolicy.REPLACE, once)
        }
    }
}
