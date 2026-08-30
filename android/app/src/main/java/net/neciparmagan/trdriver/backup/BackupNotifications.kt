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
