package net.neciparmagan.trdriver.backup

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.OutOfQuotaPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import kotlinx.coroutines.CancellationException
import net.neciparmagan.trdriver.R
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.LocalMedia
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.MediaCatalog
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadConflictPolicy
import net.neciparmagan.trdriver.data.UploadExecutor
import net.neciparmagan.trdriver.data.UploadNetworkBlockedException
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.data.UploadRetry
import net.neciparmagan.trdriver.data.UploadedMediaDb
import net.neciparmagan.trdriver.upload.UploadForegroundService
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
        val hasMedia = MediaAccess.hasMediaAccess(applicationContext)
        val hasFolders = session.backupFolderUris.isNotEmpty()
        if (!hasMedia && !hasFolders) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = 0,
                pendingCount = 0,
                message = "Yedek: galeri izni yok — Ayarlar → Yedek’ten fotoğraf/video izni verin",
                clearFileBytes = true,
            )
            BackupStatusWidget.refreshAll(applicationContext)
            return Result.success()
        }
        if (!session.backupOnWifi && !session.backupOnMobile) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount,
                message = "Yedek: ağ kapalı — Wi‑Fi veya mobil veriyi açın",
                clearFileBytes = true,
            )
            BackupStatusWidget.refreshAll(applicationContext)
            return Result.success()
        }
        val db = UploadedMediaDb(applicationContext)
        val api = DriveApi(session, applicationContext)
        val continueDelaySec = continueDelaySeconds(applicationContext, session)
        return try {
            setForeground(createForegroundInfo(session, "Galeri yedekleme…"))
            val gallery = if (hasMedia) {
                MediaCatalog.scan(applicationContext, limit = 4000)
            } else {
                emptyList()
            }
            val folders = MediaCatalog.scanDocumentTrees(
                applicationContext,
                session.backupFolderUris,
                limitPerTree = 800,
            )
            val media = gallery + folders
            val eligible = media.filter { it.sizeBytes !in 1 until 1024 }
            val pendingList = eligible.filter { !db.isUploaded(it.mediaKey) }
            var alreadyDone = (eligible.size - pendingList.size).coerceAtLeast(0)

            if (pendingList.isEmpty()) {
                val hint = when {
                    !hasMedia && hasFolders -> " (yalnız ek klasörler; galeri izni yok)"
                    hasMedia && eligible.isEmpty() -> " (tarama boş — izin veya medya yok)"
                    else -> ""
                }
                session.updateBackupProgress(
                    active = false,
                    currentFile = "",
                    doneCount = alreadyDone,
                    pendingCount = 0,
                    message = "Yedek: yeni öğe yok (${eligible.size} tarandı)$hint",
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
                    if (!UploadNetworkGate.allowsUploadNow(applicationContext, session, item.sizeBytes)) {
                        UploadNetworkGate.awaitUploadAllowed(applicationContext, session, item.sizeBytes)
                    }
                    val parentKey = parentCacheKey(item)
                    val parent = parentCache.getOrPut(parentKey) { resolveParent(api, item) }
                    val lastEmitMs = AtomicLong(0L)
                    val entry = UploadExecutor.uploadMediaItem(
                        context = applicationContext,
                        api = api,
                        session = session,
                        parentId = parent,
                        media = item,
                        conflict = UploadConflictPolicy.RENAME,
                        onProgress = { sent, total ->
                            session.updateBackupFileBytes(sent, total)
                            val now = System.currentTimeMillis()
                            if (now - lastEmitMs.get() >= 500L || sent >= total) {
                                lastEmitMs.set(now)
                                if (now - lastWidgetRefresh >= 1_500L || sent >= total) {
                                    lastWidgetRefresh = now
                                    BackupStatusWidget.refreshAll(applicationContext)
                                }
                            }
                        },
                        onRetry = { attempt, _ ->
                            session.updateBackupProgress(
                                active = true,
                                currentFile = item.displayName,
                                doneCount = alreadyDone,
                                pendingCount = pendingLeft,
                                message = "Ağ değişti, yeniden ($attempt) · ${item.displayName}",
                            )
                        },
                    )
                    db.markUploaded(item.mediaKey, entry.id, item.sizeBytes, item.uri)
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
                } catch (e: UploadNetworkBlockedException) {
                    session.updateBackupProgress(
                        active = false,
                        currentFile = item.displayName,
                        doneCount = alreadyDone,
                        pendingCount = pendingLeft,
                        message = e.reason,
                        clearFileBytes = true,
                    )
                    BackupStatusWidget.refreshAll(applicationContext)
                    scheduleContinue(applicationContext, delaySeconds = 30)
                    return Result.success()
                } catch (e: Exception) {
                    Log.w(TAG, "upload failed ${item.displayName}: ${e.message}")
                    session.updateBackupProgress(
                        active = false,
                        currentFile = item.displayName,
                        doneCount = alreadyDone,
                        pendingCount = pendingLeft,
                        message = if (UploadRetry.isTransient(e)) {
                            "Yedek bekliyor (ağ): ${item.displayName}"
                        } else {
                            "Yedek hata (${item.displayName}): ${e.message}"
                        },
                        clearFileBytes = true,
                    )
                    BackupStatusWidget.refreshAll(applicationContext)
                    if (UploadRetry.isTransient(e)) {
                        scheduleContinue(applicationContext, delaySeconds = 15)
                        return Result.success()
                    }
                    // Permanent error: skip file, continue batch
                    pendingLeft -= 1
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
    }

    private fun createForegroundInfo(session: SessionStore, text: String): ForegroundInfo {
        val channelId = UploadForegroundService.CHANNEL_ID
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.O) {
            val nm = applicationContext.getSystemService(android.app.NotificationManager::class.java)
            nm.createNotificationChannel(
                android.app.NotificationChannel(
                    channelId,
                    "Yedekleme",
                    android.app.NotificationManager.IMPORTANCE_LOW,
                ),
            )
        }
        val notification = NotificationCompat.Builder(applicationContext, channelId)
            .setSmallIcon(R.mipmap.ic_launcher)
            .setContentTitle("TR Driver yedek")
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
        return ForegroundInfo(4101, notification)
    }
}
