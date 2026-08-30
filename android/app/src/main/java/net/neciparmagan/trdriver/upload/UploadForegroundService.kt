package net.neciparmagan.trdriver.upload

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.MainActivity
import net.neciparmagan.trdriver.R
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.SkipUploadException
import net.neciparmagan.trdriver.data.UploadConflictPolicy
import net.neciparmagan.trdriver.data.UploadConflictUi
import net.neciparmagan.trdriver.data.UploadExecutor
import net.neciparmagan.trdriver.data.UploadNetworkBlockedException
import net.neciparmagan.trdriver.data.UploadQueueDb
import net.neciparmagan.trdriver.data.UploadRetry

/**
 * Keeps uploads running in background (intake, manual gallery) with a foreground notification.
 */
class UploadForegroundService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var worker: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopSelf()
                return START_NOT_STICKY
            }
        }
        ensureChannel()
        val notification = buildNotification("Yükleme hazırlanıyor…", 0, 1)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
        worker?.cancel()
        worker = scope.launch { drainQueue() }
        return START_STICKY
    }

    override fun onDestroy() {
        worker?.cancel()
        scope.cancel()
        SessionStore(this).clearUploadQueueProgress()
        super.onDestroy()
    }

    private suspend fun drainQueue() {
        val session = SessionStore(this)
        val db = UploadQueueDb(this)
        val api = DriveApi(session, applicationContext)
        var total = db.countPending().coerceAtLeast(1)
        var done = 0
        while (true) {
            val row = db.nextPending() ?: break
            total = db.countPending() + done + 1
            session.updateUploadQueueProgress(
                active = true,
                currentFile = row.displayName,
                done = done,
                pending = db.countPending(),
                message = "Yükleniyor: ${row.displayName}",
            )
            updateNotification(row.displayName, done, total)
            val policy = runCatching {
                UploadConflictPolicy.valueOf(row.conflictPolicy.uppercase())
            }.getOrDefault(UploadConflictPolicy.ASK)
            try {
                UploadExecutor.uploadUri(
                    context = this,
                    api = api,
                    session = session,
                    parentId = row.parentId,
                    uri = Uri.parse(row.localUri),
                    displayName = row.displayName,
                    conflict = policy,
                    onProgress = { sent, totalBytes ->
                        session.updateUploadQueueFileBytes(sent, totalBytes)
                    },
                    onRetry = { attempt, _ ->
                        session.updateUploadQueueProgress(
                            active = true,
                            currentFile = row.displayName,
                            done = done,
                            pending = db.countPending(),
                            message = "Ağ değişti, yeniden ($attempt) · ${row.displayName}",
                        )
                    },
                    onConflictAsk = UploadConflictUi.askHandler ?: { UploadConflictPolicy.RENAME },
                )
                db.markDone(row.id)
                done++
            } catch (e: SkipUploadException) {
                db.markDone(row.id)
                done++
            } catch (e: UploadNetworkBlockedException) {
                db.markFailed(row.id, e.reason)
                session.updateUploadQueueProgress(
                    active = false,
                    currentFile = row.displayName,
                    done = done,
                    pending = db.countPending(),
                    message = e.reason,
                )
                delayAndRetryNotification(e.reason)
                return
            } catch (e: Exception) {
                if (UploadRetry.isTransient(e) && row.attempts < 8) {
                    db.markFailed(row.id, e.message ?: "hata")
                    delayAndRetryNotification("Yeniden denenecek: ${row.displayName}")
                    return
                }
                db.markFailed(row.id, e.message ?: "hata")
                done++
            }
        }
        session.updateUploadQueueProgress(
            active = false,
            currentFile = "",
            done = done,
            pending = 0,
            message = if (done > 0) "Yükleme tamam ($done)" else "Yükleme kuyruğu boş",
            clearBytes = true,
        )
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private suspend fun delayAndRetryNotification(reason: String) {
        updateNotification(reason, 0, 1)
        kotlinx.coroutines.delay(5_000)
        startDrain(this)
    }

    private fun updateNotification(text: String, done: Int, total: Int) {
        val nm = getSystemService(NotificationManager::class.java)
        nm.notify(NOTIFICATION_ID, buildNotification(text, done, total.coerceAtLeast(1)))
    }

    private fun buildNotification(text: String, done: Int, total: Int): Notification {
        val open = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(net.neciparmagan.trdriver.R.drawable.ic_stat_backup)
            .setContentTitle("TR Driver yükleme")
            .setContentText(text)
            .setSubText(if (total > 1) "$done / $total" else null)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
            .build()
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "Yüklemeler",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description = "Arka plan dosya yüklemeleri"
            },
        )
    }

    companion object {
        const val CHANNEL_ID = "trdriver_uploads"
        const val NOTIFICATION_ID = 4102
        private const val ACTION_STOP = "stop"

        fun enqueue(
            context: Context,
            source: String,
            parentId: String,
            items: List<Pair<Uri, String>>,
            conflict: UploadConflictPolicy = UploadConflictPolicy.ASK,
        ) {
            if (items.isEmpty()) return
            val db = UploadQueueDb(context.applicationContext)
            for ((uri, name) in items) {
                db.enqueue(source, parentId, uri.toString(), name, conflict)
            }
            startDrain(context)
        }

        fun startDrain(context: Context) {
            val intent = Intent(context.applicationContext, UploadForegroundService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.applicationContext.startForegroundService(intent)
            } else {
                context.applicationContext.startService(intent)
            }
        }
    }
}
