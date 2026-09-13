package net.neciparmagan.trdriver.backup

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.util.Log
import net.neciparmagan.trdriver.data.SessionStore

/**
 * Lightweight sticky foreground service so backup/watchers survive Home button.
 * Does not block Wi‑Fi; actual uploads stay throttled in GalleryBackupEngine.
 */
class TrKeepAliveService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        BackupNotifications.ensureChannel(this)
        val n = BackupNotifications.build(
            this,
            "TR Driver",
            "Arka planda hazır · yedek yalnız Wi‑Fi",
        )
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                startForeground(
                    BackupNotifications.KEEPALIVE_NOTIFICATION_ID,
                    n,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
                )
            } else {
                startForeground(BackupNotifications.KEEPALIVE_NOTIFICATION_ID, n)
            }
        } catch (e: Exception) {
            Log.w(TAG, "startForeground failed: ${e.message}")
        }
        return START_STICKY
    }

    companion object {
        private const val TAG = "TrKeepAlive"
        fun startIfNeeded(context: Context) {
            val session = SessionStore(context)
            if (!session.isLoggedIn || !session.anyBackupEnabled()) {
                stop(context)
                return
            }
            val app = context.applicationContext
            val i = Intent(app, TrKeepAliveService::class.java)
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    app.startForegroundService(i)
                } else {
                    app.startService(i)
                }
            } catch (e: Exception) {
                Log.w(TAG, "keep-alive start failed", e)
            }
        }

        fun stop(context: Context) {
            runCatching {
                context.applicationContext.stopService(Intent(context, TrKeepAliveService::class.java))
            }
        }
    }
}
