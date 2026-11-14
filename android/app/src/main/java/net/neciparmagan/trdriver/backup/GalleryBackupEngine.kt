package net.neciparmagan.trdriver.backup

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
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
    val uploadedCount: Int = 0,
)

/** Core gallery backup logic — safe to run from WorkManager or the persistent foreground service. */
object GalleryBackupEngine {
    private const val TAG = "GalleryBackupEngine"
    private const val FILES_PER_RUN = 8
    private const val SCAN_CACHE_MS = 60_000L

    // A full SAF tree walk is slow (one ContentProvider round-trip per entry),
    // and a cycle runs many batches. Cache the eligible list briefly; the
    // uploaded-keys filter below is always read fresh, so this can never
    // re-upload an item or skip a newly taken photo for more than a minute.
    private var cachedEligible: List<LocalMedia>? = null
    private var cachedEligibleAt = 0L

    fun invalidateScanCache() {
        cachedEligible = null
        cachedEligibleAt = 0L
    }

    /**
     * Serialise every caller. Without this, the keep-alive service, the
     * WorkManager fallback and a manual "Yedekle" tap can upload the same file
     * at the same time when boot + Wi-Fi + periodic triggers overlap.
     */
    suspend fun runBatch(
        context: Context,
        isStopped: () -> Boolean = { false },
        onNotification: ((String) -> Unit)? = null,
    ): GalleryBackupBatchResult {
        if (!BackupRunLock.tryAcquire()) {
            return GalleryBackupBatchResult(
                morePending = true,
                scheduleContinue = true,
                continueDelaySec = 10L,
                message = "Başka bir yedek oturumu çalışıyor",
            )
        }
        return try {
            runBatchInner(context, isStopped, onNotification)
        } finally {
            BackupRunLock.release()
        }
    }

    private suspend fun runBatchInner(
        context: Context,
        isStopped: () -> Boolean,
        onNotification: ((String) -> Unit)?,
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
        if (!UploadNetworkGate.isWifi(app)) {
            session.updateBackupProgress(
                active = false,
                currentFile = "",
                doneCount = session.backupDoneCount,
                pendingCount = session.backupPendingCount.coerceAtLeast(1),
                message = "Wi‑Fi bekleniyor (mobil veri ile yedekleme kapalı)",
                clearFileBytes = true,
            )
            BackupStatusWidget.refreshAll(app)
            return GalleryBackupBatchResult(
                morePending = true,
                scheduleContinue = true,
                continueDelaySec = 30L,
                message = "Wi‑Fi bekleniyor",
            )
        }

        val db = UploadedMediaDb(app)
        val api = DriveApi(session, app)

        // Full library scan. The old 4000-item / 800-per-tree caps meant older
        // photos were NEVER backed up on large libraries.
        val eligible = scanEligible(app, session, hasMedia)

        // One read instead of one SQLite query per media item (was O(n) queries).
        val uploadedKeys = db.allUploadedKeys()
        val pendingList = eligible.filter { it.mediaKey !in uploadedKeys }
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
        var uploadedCount = 0
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
                    uploadedCount = uploadedCount,
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
                if (!UploadNetworkGate.allowsUploadNow(app, session, item.sizeBytes, wifiOnly = true)) {
                    UploadNetworkGate.awaitUploadAllowed(app, session, item.sizeBytes, wifiOnly = true)
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
                if (entry.id.isNotBlank() && item.mediaKey.isNotBlank()) {
                    val size = item.sizeBytes.coerceAtLeast(1L)
                    db.markUploaded(item.mediaKey, entry.id, size, item.uri)
                }
                uploadedCount++
                alreadyDone += 1
                pendingLeft -= 1
                session.updateBackupProgress(
                    active = pendingLeft > 0,
                    currentFile = if (pendingLeft > 0) "" else item.displayName,
                    doneCount = alreadyDone,
                    pendingCount = pendingLeft,
                    message = if (pendingLeft > 0) {
                        "Yedek OK (+1). Kalan ~$pendingLeft · Wi‑Fi sınırlı hız"
                    } else {
                        "Yedek tamam. Toplam işaretli: ${db.countUploaded()}"
                    },
                    clearFileBytes = true,
                )
                // Leave Wi‑Fi free for WhatsApp / YouTube / other apps between files.
                if (pendingLeft > 0) delay(2_500L)
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
                    uploadedCount = uploadedCount,
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
                        uploadedCount = uploadedCount,
                    )
                }
                pendingLeft -= 1
            }
        }

        if (pendingLeft <= 0) {
            // Fully backed up as of now — drives the "son yedek" line.
            session.lastBackupSuccessAt = System.currentTimeMillis()
        }
        BackupStatusWidget.refreshAll(app)
        return GalleryBackupBatchResult(
            morePending = pendingLeft > 0,
            scheduleContinue = pendingLeft > 0,
            continueDelaySec = 2L,
            uploadedCount = uploadedCount,
        )
    }

    private fun scanEligible(app: Context, session: SessionStore, hasMedia: Boolean): List<LocalMedia> {
        val now = System.currentTimeMillis()
        cachedEligible?.let { if (now - cachedEligibleAt < SCAN_CACHE_MS) return it }
        val gallery = if (hasMedia) MediaCatalog.scanAll(app) else emptyList()
        val folders = MediaCatalog.scanAllDocumentTrees(app, session.backupFolderUris)
        // Only skip genuinely empty/placeholder items; small text/config files matter too.
        val eligible = (gallery + folders).filter { it.sizeBytes > 0L }
        cachedEligible = eligible
        cachedEligibleAt = now
        return eligible
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
