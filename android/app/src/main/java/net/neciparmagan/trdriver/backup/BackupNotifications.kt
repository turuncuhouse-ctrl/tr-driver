package net.neciparmagan.trdriver.backup

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.work.ForegroundInfo
import net.neciparmagan.trdriver.BackupSettingsActivity
import net.neciparmagan.trdriver.R

object BackupNotifications {
    const val CHANNEL_ID = "trdriver_gallery_backup"
    const val WORKER_NOTIFICATION_ID = 4101
    const val SERVICE_NOTIFICATION_ID = 4103
    const val KEEPALIVE_NOTIFICATION_ID = 4105
    const val HEALTH_NOTIFICATION_ID = 4107
    const val HEALTH_CHANNEL_ID = "trdriver_backup_health"

    fun ensureChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "Galeri yedekleme",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description = "Arka plan fotoğraf ve video yedeklemesi"
                setShowBadge(false)
            },
        )
    }

    fun build(context: Context, title: String, text: String, progress: Int? = null): Notification {
        ensureChannel(context)
        val open = PendingIntent.getActivity(
            context,
            0,
            Intent(context, BackupSettingsActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_SINGLE_TOP
            },
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val builder = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_backup)
            .setContentTitle(title)
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
        if (progress != null) {
            builder.setProgress(100, progress.coerceIn(0, 100), false)
        }
        return builder.build()
    }

    /**
     * Loud, once-a-day alert when the backup has been stalled for a long time.
     * The normal channel is IMPORTANCE_LOW and silent; a stopped backup must
     * never fail quietly.
     */
    fun notifyStalled(context: Context) {
        val app = context.applicationContext
        val prefs = app.getSharedPreferences("trdriver_backup_health", Context.MODE_PRIVATE)
        val now = System.currentTimeMillis()
        if (now - prefs.getLong("last_stall_alert_at", 0L) < 24L * 60L * 60L * 1000L) return
        prefs.edit().putLong("last_stall_alert_at", now).apply()

        val nm = app.getSystemService(NotificationManager::class.java) ?: return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(
                    HEALTH_CHANNEL_ID,
                    "Yedekleme uyarıları",
                    NotificationManager.IMPORTANCE_DEFAULT,
                ).apply { description = "Yedekleme uzun süredir ilerlemiyor" },
            )
        }
        val open = PendingIntent.getActivity(
            app,
            0,
            Intent(app, BackupSettingsActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_SINGLE_TOP
            },
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val n = NotificationCompat.Builder(app, HEALTH_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_backup)
            .setContentTitle("TR Driver yedeği durmuş olabilir")
            .setContentText("Uzun süredir yedekleme yok · pil/otomatik başlatma izinlerini kontrol edin")
            .setContentIntent(open)
            .setAutoCancel(true)
            .setOnlyAlertOnce(true)
            .build()
        nm.notify(HEALTH_NOTIFICATION_ID, n)
    }

    fun workerForegroundInfo(context: Context, text: String): ForegroundInfo {
        val notification = build(context, "TR Driver yedek", text)
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ForegroundInfo(
                WORKER_NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
            )
        } else {
            ForegroundInfo(WORKER_NOTIFICATION_ID, notification)
        }
    }
}
