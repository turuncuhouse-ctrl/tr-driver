package net.neciparmagan.trdriver.backup

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
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
        val continueDelaySec = continueDelaySeconds(applicationContext, session)
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

            val pace = runCatching { api.refreshUploadPace(force = true) }.getOrNull()
            val boosted = ((pace?.recommendedBatch ?: FILES_PER_RUN) * 1.25).toInt()
            val filesPerRun = boosted.coerceIn(1, 25)
            val batch = pendingList.take(filesPerRun)
            var pendingLeft = pendingList.size
            var lastWidgetRefresh = 0L
            val parentCache = mutableMapOf<String, String>()

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
                    scheduleContinue(applicationContext, delaySeconds = continueDelaySec)
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
                if (lastWidgetRefresh == 0L) {
                    BackupStatusWidget.refreshAll(applicationContext)
                }

                try {
                    val parentKey = parentCacheKey(item)
                    val parent = parentCache.getOrPut(parentKey) { resolveParent(api, item) }
                    val lastEmitMs = AtomicLong(0L)
                    val entry = api.uploadMedia(parent, item, onProgress = { sent, total ->
                        session.updateBackupFileBytes(sent, total)
                        val now = System.currentTimeMillis()
                        if (now - lastEmitMs.get() >= 500L || sent >= total) {
                            lastEmitMs.set(now)
                            if (now - lastWidgetRefresh >= 1_500L || sent >= total) {
                                lastWidgetRefresh = now
                                BackupStatusWidget.refreshAll(applicationContext)
                            }
                        }
                    })
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
                        scheduleContinue(applicationContext, delaySeconds = continueDelaySec)
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
                scheduleContinue(applicationContext, delaySeconds = continueDelaySec)
            }
            BackupStatusWidget.refreshAll(applicationContext)
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
                scheduleContinue(applicationContext, delaySeconds = continueDelaySec)
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

    private fun parentCacheKey(item: LocalMedia): String {
        val label = item.backupFolderLabel
        return if (!label.isNullOrBlank()) {
            "saf:$label"
        } else {
            "photo:${item.year}-${item.month}"
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
        private const val FILES_PER_RUN = 19
        const val UNIQUE_PERIODIC = "trdriver_gallery_periodic"
        const val UNIQUE_ONCE = "trdriver_gallery_once"
        const val UNIQUE_CONTINUE = "trdriver_gallery_continue"

        private fun continueDelaySeconds(context: Context, session: SessionStore): Long {
            if (!session.wifiOnlyBackup) return 2L
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

        fun scheduleContinue(context: Context, delaySeconds: Long = 1) {
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
