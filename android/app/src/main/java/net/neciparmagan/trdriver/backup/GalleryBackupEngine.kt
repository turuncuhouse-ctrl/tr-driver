package net.neciparmagan.trdriver.backup

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CancellationException
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
import net.neciparmagan.trdriver.widget.BackupStatusWidget
import java.util.concurrent.atomic.AtomicLong

data class GalleryBackupBatchResult(
    val morePending: Boolean,
    val scheduleContinue: Boolean,
    val continueDelaySec: Long = 2L,
    val message: String = "",
)

/** Core gallery backup logic — safe to run from WorkManager or foreground service. */
object GalleryBackupEngine {
    private const val TAG = "GalleryBackupEngine"
    private const val FILES_PER_RUN = 19

    suspend fun runBatch(
        context: Context,
        isStopped: () -> Boolean = { false },
        onNotification: ((String) -> Unit)? = null,
    ): GalleryBackupBatchResult {
        val app = context.applicationContext
        val session = SessionStore(app)
        if (!session.isLoggedIn || !session.galleryBackupEnabled) {
            session.clearBackupProgress()
            BackupStatusWidget.refreshAll(app)
            return GalleryBackupBatchResult(morePending = false, scheduleContinue = false)
        }
        val hasMedia = MediaAccess.hasMediaAccess(app)
        val hasFolders = session.backupFolderUris.isNotEmpty()
        if (!hasMedia && !hasFolders) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = 0,
                pendingCount = 0,
                message = "Yedek: galeri izni yok — Ayarlar → Yedek’ten izin verin",
                clearFileBytes = true,
            )
            BackupStatusWidget.refreshAll(app)
            return GalleryBackupBatchResult(morePending = false, scheduleContinue = false)
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
            BackupStatusWidget.refreshAll(app)
            return GalleryBackupBatchResult(morePending = false, scheduleContinue = false)
        }

        val db = UploadedMediaDb(app)
        val api = DriveApi(session, app)
        val gallery = if (hasMedia) MediaCatalog.scan(app, limit = 4000) else emptyList()
        val folders = MediaCatalog.scanDocumentTrees(app, session.backupFolderUris, limitPerTree = 800)
        val eligible = (gallery + folders).filter { it.sizeBytes !in 1 until 1024 }
        val pendingList = eligible.filter { !db.isUploaded(it.mediaKey) }
        var alreadyDone = (eligible.size - pendingList.size).coerceAtLeast(0)

        if (pendingList.isEmpty()) {
            val partialHint = if (MediaAccess.hasPartialMediaAccess(app)) " · sınırlı galeri izni" else ""
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = alreadyDone,
                pendingCount = 0,
                message = "Yedek: yeni öğe yok (${eligible.size} tarandı)$partialHint",
                clearFileBytes = true,
            )
            BackupStatusWidget.refreshAll(app)
            return GalleryBackupBatchResult(morePending = false, scheduleContinue = false)
        }

        val pace = runCatching { api.refreshUploadPace(force = true) }.getOrNull()
        val filesPerRun = ((pace?.recommendedBatch ?: FILES_PER_RUN) * 1.25).toInt().coerceIn(1, 25)
        val batch = pendingList.take(filesPerRun)
        var pendingLeft = pendingList.size
        var lastWidgetRefresh = 0L
        val parentCache = mutableMapOf<String, String>()

        for (item in batch) {
            if (isStopped()) {
                session.updateBackupProgress(
                    active = false,
                    currentFile = "",
                    doneCount = alreadyDone,
                    pendingCount = pendingLeft,
                    message = "Yedek duraklatıldı; kısa süre sonra devam",
                    clearFileBytes = true,
                )
                BackupStatusWidget.refreshAll(app)
                return GalleryBackupBatchResult(
                    morePending = pendingLeft > 0,
                    scheduleContinue = true,
                    continueDelaySec = 2L,
                )
            }

            onNotification?.invoke("Yedekleniyor: ${item.displayName}")
            session.updateBackupProgress(
                active = true,
                currentFile = item.displayName,
                doneCount = alreadyDone,
                pendingCount = pendingLeft,
                message = "Yedekleniyor: ${item.displayName}",
            )
            session.updateBackupFileBytes(0L, item.sizeBytes.coerceAtLeast(0L))
            if (lastWidgetRefresh == 0L) BackupStatusWidget.refreshAll(app)

            try {
                if (!UploadNetworkGate.allowsUploadNow(app, session, item.sizeBytes)) {
                    UploadNetworkGate.awaitUploadAllowed(app, session, item.sizeBytes)
                }
                val parent = parentCache.getOrPut(parentCacheKey(item)) { resolveParent(api, item) }
                val lastEmitMs = AtomicLong(0L)
                val entry = UploadExecutor.uploadMediaItem(
                    context = app,
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
                                BackupStatusWidget.refreshAll(app)
                            }
                            if (total > 0) {
                                val pct = ((sent * 100L) / total).toInt().coerceIn(0, 100)
                                onNotification?.invoke("${item.displayName} · %$pct")
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
                        onNotification?.invoke("Ağ değişti ($attempt) · ${item.displayName}")
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
                BackupStatusWidget.refreshAll(app)
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
                BackupStatusWidget.refreshAll(app)
                return GalleryBackupBatchResult(
                    morePending = pendingLeft > 0,
                    scheduleContinue = true,
                    continueDelaySec = 30L,
                    message = e.reason,
                )
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
                BackupStatusWidget.refreshAll(app)
                if (UploadRetry.isTransient(e)) {
                    return GalleryBackupBatchResult(
                        morePending = pendingLeft > 0,
                        scheduleContinue = true,
                        continueDelaySec = 15L,
                    )
                }
                pendingLeft -= 1
            }
        }

        BackupStatusWidget.refreshAll(app)
        return GalleryBackupBatchResult(
            morePending = pendingLeft > 0,
            scheduleContinue = pendingLeft > 0,
            continueDelaySec = 2L,
        )
    }

    private fun parentCacheKey(item: LocalMedia): String {
        val label = item.backupFolderLabel
        return if (!label.isNullOrBlank()) "saf:$label" else "photo:${item.year}-${item.month}"
    }

    private suspend fun resolveParent(api: DriveApi, item: LocalMedia): String {
        val label = item.backupFolderLabel
        return if (!label.isNullOrBlank()) api.ensureBackupFolder(label) else api.ensurePhotosAlbumFolder(item)
    }
}
