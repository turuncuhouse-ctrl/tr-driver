package net.neciparmagan.trdriver.backup

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import net.neciparmagan.trdriver.data.SessionStore

/**
 * Re-arm the persistent backup service after every kind of restart.
 *
 * HyperOS/MIUI clears deferred WorkManager jobs and kills background processes.
 * BOOT_COMPLETED is one of the few remaining Android 12+ exceptions that still
 * allow a foreground-service launch, so we use it to bring the keep-alive
 * service back without requiring the user to open the app.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        val action = intent?.action ?: return
        if (action !in RESTART_ACTIONS) return
        val app = context.applicationContext
        val session = SessionStore(app)
        if (!session.isLoggedIn || !session.anyBackupEnabled()) {
            BackupController.ensure(app)
            return
        }
        try {
            BackupController.ensure(app, startForegroundService = true)
        } catch (e: Exception) {
            // Never let a boot-time exception leave WorkManager unscheduled.
            Log.w(TAG, "boot re-arm with FGS failed, falling back to WorkManager", e)
            BackupController.ensure(app)
        }
    }

    companion object {
        private const val TAG = "BootReceiver"

        private val RESTART_ACTIONS = setOf(
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_LOCKED_BOOT_COMPLETED,
            Intent.ACTION_MY_PACKAGE_REPLACED,
            // OEM-specific quick-boot / reboot broadcasts (Xiaomi, HTC, Samsung).
            "android.intent.action.QUICKBOOT_POWERON",
            "com.htc.intent.action.QUICKBOOT_POWERON",
            "android.intent.action.REBOOT",
        )
    }
}
