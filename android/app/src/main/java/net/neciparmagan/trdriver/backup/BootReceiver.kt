package net.neciparmagan.trdriver.backup

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import net.neciparmagan.trdriver.data.SessionStore

/** Reschedule gallery backup after reboot (HyperOS clears deferred work). */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        if (intent?.action != Intent.ACTION_BOOT_COMPLETED &&
            intent?.action != Intent.ACTION_MY_PACKAGE_REPLACED
        ) {
            return
        }
        val session = SessionStore(context.applicationContext)
        if (session.isLoggedIn && session.galleryBackupEnabled) {
            GalleryBackupWorker.schedule(context.applicationContext)
        }
    }
}
