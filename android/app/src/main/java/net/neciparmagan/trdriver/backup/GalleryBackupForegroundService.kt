package net.neciparmagan.trdriver.backup

import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.IBinder
import android.util.Log
import android.content.pm.ServiceInfo
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Reliable gallery backup on HyperOS / Android 14+ — runs with a typed foreground service.
 */
class GalleryBackupForegroundService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var worker: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                worker?.cancel()
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }
        }
        BackupNotifications.ensureChannel(this)
        val notification = BackupNotifications.build(this, "TR Driver yedek", "Galeri yedekleme hazırlanıyor…")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                BackupNotifications.SERVICE_NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
            )
        } else {
            startForeground(BackupNotifications.SERVICE_NOTIFICATION_ID, notification)
        }
        if (worker?.isActive == true) {
            return START_STICKY
        }
        worker = scope.launch { runBackupLoop() }
        return START_STICKY
    }

    override fun onDestroy() {
        worker?.cancel()
        scope.cancel()
        running = false
        super.onDestroy()
    }

    private suspend fun runBackupLoop() {
        running = true
        try {
            var loops = 0
            while (loops < 24) {
                loops++
                val result = GalleryBackupEngine.runBatch(
                    context = this,
                    isStopped = { worker?.isActive != true },
                    onNotification = { text ->
                        val nm = getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager
                        nm.notify(
                            BackupNotifications.SERVICE_NOTIFICATION_ID,
                            BackupNotifications.build(this, "TR Driver yedek", text),
                        )
                    },
                )
                if (!result.scheduleContinue || !result.morePending) break
                delay(result.continueDelaySec.coerceIn(1L, 60L) * 1000L)
            }
        } catch (e: Exception) {
            Log.e(TAG, "backup loop failed", e)
        } finally {
            running = false
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
        }
    }

    companion object {
        private const val TAG = "GalleryBackupFGS"
        private const val ACTION_STOP = "stop"
        @Volatile
        var running: Boolean = false
            private set

        fun start(context: Context) {
            val app = context.applicationContext
            val intent = Intent(app, GalleryBackupForegroundService::class.java)
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    app.startForegroundService(intent)
                } else {
                    app.startService(intent)
                }
            } catch (e: Exception) {
                Log.e(TAG, "startForegroundService failed", e)
                throw e
            }
        }

        fun tryStart(context: Context): Boolean {
            return try {
                start(context)
                true
            } catch (e: Exception) {
                Log.w(TAG, "tryStart failed", e)
                false
            }
        }
    }
}
