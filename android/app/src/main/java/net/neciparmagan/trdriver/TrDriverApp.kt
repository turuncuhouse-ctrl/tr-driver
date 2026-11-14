package net.neciparmagan.trdriver

import android.app.Application
import net.neciparmagan.trdriver.backup.BackupController
import net.neciparmagan.trdriver.backup.WifiBackupWatcher
import net.neciparmagan.trdriver.data.SessionStore

class TrDriverApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val session = SessionStore(this)
        // Automatic backup stays Wi-Fi-only; mobile data never used silently.
        session.wifiOnlyBackup = true
        session.disableCommsBackup()

        WifiBackupWatcher.start(this)

        // No foreground-service launch here: Application.onCreate also runs for
        // boot receivers, and Android 12+ refuses an FGS started that way.
        // BootReceiver (BOOT_COMPLETED exception) and Activity resumes start it.
        BackupController.ensure(this)
    }
}
