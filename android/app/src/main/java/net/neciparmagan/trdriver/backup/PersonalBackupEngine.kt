package net.neciparmagan.trdriver.backup

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import net.neciparmagan.trdriver.data.CommsAccess
import net.neciparmagan.trdriver.data.ContactsBackupSource
import net.neciparmagan.trdriver.data.HttpStatusIOException
import net.neciparmagan.trdriver.data.PersonalBackupApi
import net.neciparmagan.trdriver.data.PersonalBackupCodec
import net.neciparmagan.trdriver.data.PersonalBackupDb
import net.neciparmagan.trdriver.data.PersonalBackupItem
import net.neciparmagan.trdriver.data.PersonalBackupMeta
import net.neciparmagan.trdriver.data.PersonalBackupPolicy
import net.neciparmagan.trdriver.data.PersonalBackupSyncRequest
import net.neciparmagan.trdriver.data.ScannedRecord
import net.neciparmagan.trdriver.data.ScanResult
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.SmsBackupSource
import net.neciparmagan.trdriver.data.SyncedItem
import net.neciparmagan.trdriver.data.UploadNetworkBlockedException
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.data.UploadRetry

/**
 * Outcome of one contacts/SMS push cycle.
 *
 * [morePending] tells the keep-alive service / worker to come back later
 * (large address book, transient network error). The engine never blocks the
 * service loop: at most [MAX_CHUNKS_PER_KIND] batches are sent per kind.
 */
data class PersonalBackupBatchResult(
    val morePending: Boolean = false,
    val sentItems: Int = 0,
    val deletedItems: Int = 0,
    val message: String = "",
    val waitingForWifi: Boolean = false,
)

/**
 * Wi-Fi-only, incremental insurance backup for contacts and SMS.
 *
 * Design rules (same as the media pipeline):
 *  - Wi-Fi / Ethernet only; mobile data never used for automatic backup,
 *  - only changed records are sent (content hash + revision),
 *  - deletions are explicit and only inferred from a complete scan,
 *  - every batch is small with a pause in between, so background sync never
 *    starves WhatsApp / YouTube and stays well inside the server rate limit,
 *  - a single global run lock prevents the foreground service and the
 *    WorkManager fallback from pushing the same rows twice.
 */
object PersonalBackupEngine {
    private const val TAG = "PersonalBackup"
    private const val MAX_CHUNKS_PER_KIND = 3
    private const val DELAY_BETWEEN_BATCHES_MS = 1_200L

    /**
     * Every few hours, while there is nothing to send, compare the local row
     * count with the server. If the server copy lost rows (purged from another
     * device, restored database), the local hashes are dropped so the next run
     * re-uploads everything instead of silently skipping.
     */
    private const val VERIFY_INTERVAL_MS = 6L * 60L * 60L * 1000L

    suspend fun runBatch(
        context: Context,
        isStopped: () -> Boolean = { false },
    ): PersonalBackupBatchResult {
        if (!BackupRunLock.tryAcquire()) {
            return PersonalBackupBatchResult(
                morePending = true,
                message = "Başka bir yedek oturumu çalışıyor",
            )
        }
        return try {
            runBatchInner(context.applicationContext, isStopped)
        } finally {
            BackupRunLock.release()
        }
    }

    private suspend fun runBatchInner(
        app: Context,
        isStopped: () -> Boolean,
    ): PersonalBackupBatchResult {
        val session = SessionStore(app)
        if (!session.isLoggedIn) {
            return PersonalBackupBatchResult(message = "Oturum gerekli")
        }
        val contactsOn = session.contactsBackupEnabled
        val smsOn = session.smsBackupEnabled
        if (!contactsOn && !smsOn) {
            return PersonalBackupBatchResult()
        }
        if (!UploadNetworkGate.isWifi(app)) {
            return PersonalBackupBatchResult(
                morePending = true,
                message = "Rehber/SMS yedeği Wi‑Fi bekliyor",
                waitingForWifi = true,
            )
        }

        val db = PersonalBackupDb(app)
        try {
            return pushKinds(app, session, db, isStopped)
        } finally {
            db.release()
        }
    }

    private suspend fun pushKinds(
        app: Context,
        session: SessionStore,
        db: PersonalBackupDb,
        isStopped: () -> Boolean,
    ): PersonalBackupBatchResult {
        val api = PersonalBackupApi(session)
        val contactsOn = session.contactsBackupEnabled
        val smsOn = session.smsBackupEnabled
        var sent = 0
        var deleted = 0
        var morePending = false
        var lastMessage = ""

        if (contactsOn) {
            val result = pushKind(
                app = app,
                session = session,
                db = db,
                api = api,
                kind = PersonalBackupPolicy.KIND_CONTACTS,
                scan = { skip -> ContactsBackupSource.read(app, skip) },
                hasPermission = { CommsAccess.hasContactsAccess(app) },
                isStopped = isStopped,
            )
            sent += result.sentItems
            deleted += result.deletedItems
            morePending = morePending || result.morePending
            if (result.message.isNotBlank()) lastMessage = result.message
            if (isStopped()) {
                return PersonalBackupBatchResult(
                    morePending = true,
                    sentItems = sent,
                    deletedItems = deleted,
                    message = lastMessage,
                )
            }
        }

        if (smsOn) {
            val result = pushKind(
                app = app,
                session = session,
                db = db,
                api = api,
                kind = PersonalBackupPolicy.KIND_SMS,
                scan = { skip -> SmsBackupSource.read(app, skip) },
                hasPermission = { CommsAccess.canReadSms(app) },
                isStopped = isStopped,
            )
            sent += result.sentItems
            deleted += result.deletedItems
            morePending = morePending || result.morePending
            if (result.message.isNotBlank()) lastMessage = result.message
        }

        return PersonalBackupBatchResult(
            morePending = morePending,
            sentItems = sent,
            deletedItems = deleted,
            message = lastMessage,
        )
    }

    private suspend fun pushKind(
        app: Context,
        session: SessionStore,
        db: PersonalBackupDb,
        api: PersonalBackupApi,
        kind: String,
        scan: ((String, String) -> Boolean) -> ScanResult,
        hasPermission: () -> Boolean,
        isStopped: () -> Boolean,
    ): PersonalBackupBatchResult {
        if (!hasPermission()) {
            db.recordMeta(kind, session.email, db.countStored(kind), "İzin yok", ok = false)
            return PersonalBackupBatchResult(message = permissionMessage(kind))
        }

        // Account switch: the hashes of another account must never be reused.
        val meta = db.meta(kind)
        if (meta.account.isNotBlank() && meta.account != session.email) {
            db.clearKind(kind)
        }

        val stored = db.storedFor(kind)
        // Records already on the server are dropped inside the scan, so only
        // changed payloads are held in memory.
        val scanned = try {
            scan { key, hash -> stored[key]?.hash == hash && hash.isNotEmpty() }
        } catch (e: Exception) {
            Log.w(TAG, "scan failed for $kind: ${e.javaClass.simpleName}")
            db.recordMeta(kind, session.email, db.countStored(kind), "Tarama başarısız", ok = false)
            return PersonalBackupBatchResult(message = "Tarama başarısız")
        }

        val pending = ArrayList<ScannedRecord>()
        var unchanged = scanned.unchanged
        for (record in scanned.records) {
            if (!PersonalBackupPolicy.isSafeKey(record.key)) continue
            val decision = PersonalBackupPolicy.decide(
                stored = stored[record.key],
                revision = record.revision,
                hash = record.hash,
            )
            if (decision == PersonalBackupPolicy.Decision.SKIP) {
                unchanged++
            } else {
                pending.add(record)
            }
        }
        val scannedKeys = scanned.keys
        val deletions = PersonalBackupPolicy.pendingDeletions(stored.keys, scannedKeys, scanned.complete)

        if (pending.isEmpty() && deletions.isEmpty()) {
            if (!scanned.complete) {
                db.recordMeta(kind, session.email, stored.size, "Kısmi tarama — değişiklik yok", ok = false)
                return PersonalBackupBatchResult(message = "Kısmi tarama (${kind})")
            }
            if (stored.isNotEmpty() && shouldVerify(meta)) {
                val serverCount = runCatching { api.status() }.getOrNull()
                    ?.kinds?.firstOrNull { it.kind == kind }?.count
                if (serverCount != null && serverCount < stored.size) {
                    db.clearKind(kind)
                    db.recordMeta(
                        kind,
                        session.email,
                        0,
                        "Sunucu kopyası eksik — yeniden yüklenecek",
                        ok = false,
                    )
                    return PersonalBackupBatchResult(
                        morePending = true,
                        message = "Sunucu kopyası eksik, yeniden yükleniyor",
                    )
                }
            }
            db.recordMeta(kind, session.email, stored.size, "Güncel · ${scanned.keys.size} kayıt", ok = true)
            return PersonalBackupBatchResult()
        }

        val chunkSize = PersonalBackupPolicy.chunkSize(kind)
        val allBatches = PersonalBackupPolicy.chunk(pending, chunkSize)
        val batches = allBatches.take(MAX_CHUNKS_PER_KIND)
        // Deletions ride along with the first batch (or alone when only rows
        // disappeared), so one request covers the common "contact edited" case.
        val allDeletionBatches = PersonalBackupPolicy.chunk(deletions, chunkSize * 4)
        val deletionBatches = allDeletionBatches.take(MAX_CHUNKS_PER_KIND)
        val totalBatches = maxOf(batches.size, deletionBatches.size)

        var sent = 0
        var deleted = 0
        // More work than one cycle may send; the service will be nudged again.
        var morePending = allBatches.size > MAX_CHUNKS_PER_KIND ||
            allDeletionBatches.size > MAX_CHUNKS_PER_KIND

        for (index in 0 until totalBatches) {
            if (isStopped()) {
                morePending = true
                break
            }
            val chunk = batches.getOrNull(index).orEmpty()
            val removed = deletionBatches.getOrNull(index).orEmpty()
            if (chunk.isEmpty() && removed.isEmpty()) continue

            val request = PersonalBackupSyncRequest(
                deviceId = session.deviceName,
                items = chunk.map { record ->
                    PersonalBackupItem(
                        key = record.key,
                        revision = record.revision,
                        hash = record.hash,
                        data = PersonalBackupCodec.toElement(record.dataJson),
                    )
                },
                deleted = removed,
            )
            try {
                val result = UploadRetry.run(context = app, attempts = 3, wifiOnly = true) {
                    api.sync(kind, request)
                }
                sent += result.accepted
                deleted += result.deleted
                // Remember what was pushed, but never when the server reported a
                // stale revision: that row must be retried, not marked as done.
                if (result.stale == 0) {
                    val confirmed = chunk
                        .filter { record -> stored[record.key]?.hash != record.hash }
                        .map { record -> SyncedItem(record.key, record.hash, record.revision) }
                    db.markSynced(confirmed, kind)
                }
                if (removed.isNotEmpty()) db.removeSynced(kind, removed)
                db.recordMeta(
                    kind = kind,
                    account = session.email,
                    total = result.total,
                    message = summary(kind, result.accepted, result.deleted, result.unchanged, result.stale),
                    ok = true,
                )
            } catch (e: CancellationException) {
                throw e
            } catch (e: UploadNetworkBlockedException) {
                return PersonalBackupBatchResult(
                    morePending = true,
                    sentItems = sent,
                    deletedItems = deleted,
                    message = e.reason,
                    waitingForWifi = true,
                )
            } catch (e: HttpStatusIOException) {
                if (e.code == 401) {
                    db.recordMeta(kind, session.email, db.countStored(kind), "Oturum gerekli", ok = false)
                    return PersonalBackupBatchResult(
                        morePending = false,
                        sentItems = sent,
                        deletedItems = deleted,
                        message = "Oturum gerekli",
                    )
                }
                if (UploadRetry.isTransient(e)) {
                    morePending = true
                    db.recordMeta(kind, session.email, db.countStored(kind), "Sunucu bekliyor", ok = false)
                    return PersonalBackupBatchResult(
                        morePending = true,
                        sentItems = sent,
                        deletedItems = deleted,
                        message = "Sunucu bekliyor (${kind})",
                    )
                }
                Log.w(TAG, "sync rejected for $kind: HTTP ${e.code}")
                db.recordMeta(kind, session.email, db.countStored(kind), "Sunucu reddetti", ok = false)
                return PersonalBackupBatchResult(
                    sentItems = sent,
                    deletedItems = deleted,
                    message = "Sunucu reddetti (${kind})",
                )
            } catch (e: Exception) {
                if (UploadRetry.isTransient(e)) {
                    db.recordMeta(kind, session.email, db.countStored(kind), "Ağ bekleniyor", ok = false)
                    return PersonalBackupBatchResult(
                        morePending = true,
                        sentItems = sent,
                        deletedItems = deleted,
                        message = "Ağ bekleniyor (${kind})",
                    )
                }
                Log.w(TAG, "sync failed for $kind: ${e.javaClass.simpleName}")
                db.recordMeta(kind, session.email, db.countStored(kind), "Yedek hatası", ok = false)
                return PersonalBackupBatchResult(
                    sentItems = sent,
                    deletedItems = deleted,
                    message = "Yedek hatası (${kind})",
                )
            }

            if (index < totalBatches - 1) {
                // Leave the link idle between batches: keeps the existing media
                // speed limit meaningful and the server well below its quota.
                delay(DELAY_BETWEEN_BATCHES_MS)
            }
        }

        if (!scanned.complete) morePending = true
        return PersonalBackupBatchResult(
            morePending = morePending,
            sentItems = sent,
            deletedItems = deleted,
            message = lastSummary(kind, sent, unchanged, scanned.complete),
        )
    }

    private fun shouldVerify(meta: PersonalBackupMeta): Boolean {
        if (meta.lastSyncAt <= 0L) return true
        return System.currentTimeMillis() - meta.lastSyncAt >= VERIFY_INTERVAL_MS
    }

    private fun permissionMessage(kind: String): String =
        if (kind == PersonalBackupPolicy.KIND_SMS) {
            "SMS izni yok (Ayarlar → Yedek)"
        } else {
            "Rehber izni yok (Ayarlar → Yedek)"
        }

    private fun summary(kind: String, accepted: Int, deleted: Int, unchanged: Int, stale: Int): String {
        val label = if (kind == PersonalBackupPolicy.KIND_SMS) "SMS" else "Rehber"
        return buildString {
            append("$label: +$accepted")
            if (deleted > 0) append(" · silinen $deleted")
            if (unchanged > 0) append(" · aynı $unchanged")
            if (stale > 0) append(" · atlanan $stale")
        }
    }

    private fun lastSummary(kind: String, sent: Int, unchanged: Int, complete: Boolean): String {
        val label = if (kind == PersonalBackupPolicy.KIND_SMS) "SMS" else "Rehber"
        val suffix = if (!complete) " · kısmi tarama" else ""
        return "$label: $sent yüklendi, $unchanged değişmedi$suffix"
    }
}
