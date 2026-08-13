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
import kotlinx.coroutines.CancellationException
import net.neciparmagan.trdriver.widget.BackupStatusWidget
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.LocalMedia
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
            session.clearBackupProgress()
            BackupStatusWidget.refreshAll(applicationContext)
            return Result.success()
        }
        val db = UploadedMediaDb(applicationContext)
        val api = DriveApi(session, applicationContext)
        return try {
            val gallery = MediaCatalog.scan(applicationContext, limit = 1000)
            val folders = MediaCatalog.scanDocumentTrees(
                applicationContext,
                session.backupFolderUris,
                limitPerTree = 400,
            )
            val media = gallery + folders
            val eligible = media.filter { it.sizeBytes !in 1 until 1024 }
            val pendingList = eligible.filter { !db.isUploaded(it.mediaKey) }
            val alreadyDone = (eligible.size - pendingList.size).coerceAtLeast(0)

            if (pendingList.isEmpty()) {
                session.updateBackupProgress(
                    active = false,
                    currentFile = "",
                    doneCount = alreadyDone,
                    pendingCount = 0,
                    message = "Yedek: yeni öğe yok (${eligible.size} tarandı)",
                )
                BackupStatusWidget.refreshAll(applicationContext)
                return Result.success()
            }

            session.updateBackupProgress(
                active = true,
                currentFile = pendingList.first().displayName,
                doneCount = alreadyDone,
                pendingCount = pendingList.size,
                message = "Yedekleniyor: ${pendingList.first().displayName}",
            )
            BackupStatusWidget.refreshAll(applicationContext)

            val item = pendingList.first()
            if (isStopped) {
                session.updateBackupProgress(
                    active = false,
                    currentFile = "",
                    doneCount = alreadyDone,
                    pendingCount = pendingList.size,
                    message = "Yedek duraklatıldı; kısa süre sonra devam",
                )
                BackupStatusWidget.refreshAll(applicationContext)
                scheduleContinue(applicationContext, delayMinutes = 1)
                return Result.success()
            }

            try {
                val parent = resolveParent(api, item)
                val entry = api.uploadMedia(parent, item)
                db.markUploaded(item.mediaKey, entry.id, item.sizeBytes)
                val pendingLeft = pendingList.size - 1
                val doneAfter = alreadyDone + 1
                session.updateBackupProgress(
                    active = pendingLeft > 0,
                    currentFile = if (pendingLeft > 0) "" else item.displayName,
                    doneCount = doneAfter,
                    pendingCount = pendingLeft,
                    message = if (pendingLeft > 0) {
                        "Yedek OK (+1). Kalan ~$pendingLeft · cihaz: ${session.deviceName}"
                    } else {
                        "Yedek tamam. Toplam işaretli: ${db.countUploaded()}"
                    },
                )
                BackupStatusWidget.refreshAll(applicationContext)
                if (pendingLeft > 0 && session.galleryBackupEnabled) {
                    scheduleContinue(applicationContext, delayMinutes = 1)
                }
                Result.success()
            } catch (e: CancellationException) {
                session.updateBackupProgress(
                    active = false,
                    currentFile = "",
                    doneCount = alreadyDone,
                    pendingCount = pendingList.size,
                    message = "Yedek duraklatıldı; devam edecek",
                )
                BackupStatusWidget.refreshAll(applicationContext)
                if (session.galleryBackupEnabled) {
                    scheduleContinue(applicationContext, delayMinutes = 1)
                }
                throw e
            } catch (e: Exception) {
                Log.w(TAG, "upload failed ${item.displayName}: ${e.message}")
                session.updateBackupProgress(
                    active = false,
                    currentFile = item.displayName,
                    doneCount = alreadyDone,
                    pendingCount = pendingList.size,
                    message = "Yedek hata (${item.displayName}): ${e.message}",
                )
                BackupStatusWidget.refreshAll(applicationContext)
                scheduleContinue(applicationContext, delayMinutes = 2)
                Result.success()
            }
        } catch (e: CancellationException) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount,
                message = "Yedek duraklatıldı; devam edecek",
            )
            BackupStatusWidget.refreshAll(applicationContext)
            if (session.galleryBackupEnabled && session.isLoggedIn) {
                scheduleContinue(applicationContext, delayMinutes = 1)
            }
            throw e
        } catch (e: Exception) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount,
                message = "Yedek hata: ${e.message}",
            )
            BackupStatusWidget.refreshAll(applicationContext)
            Log.e(TAG, "backup failed", e)
            Result.retry()
        }
    }

    private suspend fun resolveParent(api: DriveApi, item: LocalMedia): String {
        val label = item.backupFolderLabel
        return if (!label.isNullOrBlank()) {
            api.ensureBackupFolder(label)
        } else {
            api.ensurePhotosAlbumFolder(item)
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
                session.clearBackupProgress(message = session.lastBackupMessage.ifBlank { "Yedek kapalı" })
                BackupStatusWidget.refreshAll(context)
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
            enqueueOnce(context, ExistingWorkPolicy.KEEP)
        }

        fun runNow(context: Context) {
            val session = SessionStore(context)
            if (!session.isLoggedIn || !session.galleryBackupEnabled) return
            session.updateBackupProgress(
                active = true,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount.coerceAtLeast(1),
                message = "Yedek kuyruğa alındı…",
            )
            BackupStatusWidget.refreshAll(context)
            enqueueOnce(context, ExistingWorkPolicy.KEEP)
        }

        fun scheduleContinue(context: Context, delayMinutes: Long = 1) {
            val session = SessionStore(context)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) return
            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints(session))
                .setInitialDelay(delayMinutes.coerceAtLeast(0), TimeUnit.MINUTES)
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(UNIQUE_CONTINUE, ExistingWorkPolicy.REPLACE, once)
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
            .setRequiredNetworkType(
                if (session.wifiOnlyBackup) NetworkType.UNMETERED else NetworkType.CONNECTED
            )
            .build()
    }
}
