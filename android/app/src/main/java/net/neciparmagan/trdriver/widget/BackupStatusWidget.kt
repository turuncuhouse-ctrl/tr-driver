package net.neciparmagan.trdriver.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews
import net.neciparmagan.trdriver.BackupSettingsActivity
import net.neciparmagan.trdriver.R
import net.neciparmagan.trdriver.data.SessionStore

class BackupStatusWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, appWidgetManager: AppWidgetManager, appWidgetIds: IntArray) {
        for (id in appWidgetIds) {
            updateAppWidget(context, appWidgetManager, id)
        }
    }

    companion object {
        fun refreshAll(context: Context) {
            val appContext = context.applicationContext
            val manager = AppWidgetManager.getInstance(appContext)
            val ids = manager.getAppWidgetIds(ComponentName(appContext, BackupStatusWidget::class.java))
            if (ids.isEmpty()) return
            for (id in ids) {
                updateAppWidget(appContext, manager, id)
            }
        }

        private fun updateAppWidget(context: Context, manager: AppWidgetManager, appWidgetId: Int) {
            val session = SessionStore(context)
            val views = RemoteViews(context.packageName, R.layout.widget_backup_status)
            views.setTextViewText(R.id.backupWidgetTitle, "TR Driver Yedek")
            views.setProgressBar(R.id.backupWidgetBar, 100, session.backupDisplayPercent, false)

            val bytes = session.backupFileBytesLabel()
            val text = when {
                !session.galleryBackupEnabled -> "Kapalı · ayarlara dokunun"
                session.backupActive && session.backupCurrentFile.isNotBlank() ->
                    buildString {
                        append("%${session.backupDisplayPercent}")
                        if (bytes.isNotBlank()) append(" · $bytes")
                        append(" · ${session.backupCurrentFile}")
                        if (session.backupPendingCount > 0) append(" · kalan ${session.backupPendingCount}")
                    }
                session.backupPendingCount > 0 ->
                    "Bekliyor · kalan ${session.backupPendingCount} · %${session.backupPercent}"
                else -> session.lastBackupMessage.ifBlank { "Açık · güncel" }
            }
            views.setTextViewText(R.id.backupWidgetText, text)

            val open = Intent(context, BackupSettingsActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
            }
            val pending = PendingIntent.getActivity(
                context,
                appWidgetId,
                open,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
            views.setOnClickPendingIntent(R.id.backupWidgetRoot, pending)
            manager.updateAppWidget(appWidgetId, views)
        }
    }
}
