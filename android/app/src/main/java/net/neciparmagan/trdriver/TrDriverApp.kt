package net.neciparmagan.trdriver

import android.app.Application
import net.neciparmagan.trdriver.backup.BackupController
import net.neciparmagan.trdriver.backup.WifiBackupWatcher
import net.neciparmagan.trdriver.calls.CallController
import net.neciparmagan.trdriver.data.SessionStore

class TrDriverApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val session = SessionStore(this)
        // Automatic backup (media, contacts, SMS) stays Wi-Fi-only; mobile data
        // is never used silently.
        session.wifiOnlyBackup = true

        WifiBackupWatcher.start(this)
        CallController.init(this)

        // No foreground-service launch here: Application.onCreate also runs for
        // boot receivers, and Android 12+ refuses an FGS started that way.
        // BootReceiver (BOOT_COMPLETED exception) and Activity resumes start it.
        BackupController.ensure(this)
    }
}
