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
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.LocalMedia
import net.neciparmagan.trdriver.data.MediaCatalog
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadedMediaDb
import net.neciparmagan.trdriver.widget.BackupStatusWidget
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong

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
            var alreadyDone = (eligible.size - pendingList.size).coerceAtLeast(0)

            if (pendingList.isEmpty()) {
                session.updateBackupProgress(
                    active = false,
                    currentFile = "",
                    doneCount = alreadyDone,
                    pendingCount = 0,
                    message = "Yedek: yeni öğe yok (${eligible.size} tarandı)",
                    clearFileBytes = true,
                )
                BackupStatusWidget.refreshAll(applicationContext)
                return Result.success()
            }

            val batch = pendingList.take(FILES_PER_RUN)
            var pendingLeft = pendingList.size
            var lastWidgetRefresh = 0L

            for (item in batch) {
                if (isStopped) {
                    session.updateBackupProgress(
                        active = false,
                        currentFile = "",
                        doneCount = alreadyDone,
                        pendingCount = pendingLeft,
                        message = "Yedek duraklatıldı; kısa süre sonra devam",
                        clearFileBytes = true,
                    )
                    BackupStatusWidget.refreshAll(applicationContext)
                    scheduleContinue(applicationContext, delaySeconds = 2)
                    return Result.success()
                }

                session.updateBackupProgress(
                    active = true,
                    currentFile = item.displayName,
                    doneCount = alreadyDone,
                    pendingCount = pendingLeft,
                    message = "Yedekleniyor: ${item.displayName}",
                )
                session.updateBackupFileBytes(0L, item.sizeBytes.coerceAtLeast(0L))
                BackupStatusWidget.refreshAll(applicationContext)

                try {
                    val parent = resolveParent(api, item)
                    val lastEmitMs = AtomicLong(0L)
                    val entry = api.uploadMedia(parent, item) { sent, total ->
                        session.updateBackupFileBytes(sent, total)
                        val now = System.currentTimeMillis()
                        if (now - lastEmitMs.get() >= 400L || sent >= total) {
                            lastEmitMs.set(now)
                            if (now - lastWidgetRefresh >= 1_000L || sent >= total) {
                                lastWidgetRefresh = now
                                BackupStatusWidget.refreshAll(applicationContext)
                            }
                        }
                    }
                    db.markUploaded(item.mediaKey, entry.id, item.sizeBytes)
                    alreadyDone += 1
                    pendingLeft -= 1
                    session.updateBackupProgress(
                        active = pendingLeft > 0,
                        currentFile = if (pendingLeft > 0) "" else item.displayName,
                        doneCount = alreadyDone,
                        pendingCount = pendingLeft,
                        message = if (pendingLeft > 0) {
                            "Yedek OK (+1). Kalan ~$pendingLeft · ${session.deviceName}"
                        } else {
                            "Yedek tamam. Toplam işaretli: ${db.countUploaded()}"
                        },
                        clearFileBytes = true,
                    )
                    BackupStatusWidget.refreshAll(applicationContext)
                } catch (e: CancellationException) {
                    session.updateBackupProgress(
                        active = false,
                        currentFile = "",
                        doneCount = alreadyDone,
                        pendingCount = pendingLeft,
                        message = "Yedek duraklatıldı; devam edecek",
                        clearFileBytes = true,
                    )
                    BackupStatusWidget.refreshAll(applicationContext)
                    if (session.galleryBackupEnabled) {
                        scheduleContinue(applicationContext, delaySeconds = 2)
                    }
                    throw e
                } catch (e: Exception) {
                    Log.w(TAG, "upload failed ${item.displayName}: ${e.message}")
                    session.updateBackupProgress(
                        active = false,
                        currentFile = item.displayName,
                        doneCount = alreadyDone,
                        pendingCount = pendingLeft,
                        message = "Yedek hata (${item.displayName}): ${e.message}",
                        clearFileBytes = true,
                    )
                    BackupStatusWidget.refreshAll(applicationContext)
                    scheduleContinue(applicationContext, delaySeconds = 15)
                    return Result.success()
                }
            }

            if (pendingLeft > 0 && session.galleryBackupEnabled) {
                scheduleContinue(applicationContext, delaySeconds = 2)
            }
            Result.success()
        } catch (e: CancellationException) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount,
                message = "Yedek duraklatıldı; devam edecek",
                clearFileBytes = true,
            )
            BackupStatusWidget.refreshAll(applicationContext)
            if (session.galleryBackupEnabled && session.isLoggedIn) {
                scheduleContinue(applicationContext, delaySeconds = 2)
            }
            throw e
        } catch (e: Exception) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount,
                message = "Yedek hata: ${e.message}",
                clearFileBytes = true,
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
        /** How many files to upload in one WorkManager run before a short pause. */
        private const val FILES_PER_RUN = 15
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
            enqueueOnce(context, ExistingWorkPolicy.REPLACE)
        }

        fun scheduleContinue(context: Context, delaySeconds: Long = 2) {
            val session = SessionStore(context)
            if (!session.galleryBackupEnabled || !session.isLoggedIn) return
            val once = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
                .setConstraints(constraints(session))
                .setInitialDelay(delaySeconds.coerceAtLeast(0), TimeUnit.SECONDS)
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
