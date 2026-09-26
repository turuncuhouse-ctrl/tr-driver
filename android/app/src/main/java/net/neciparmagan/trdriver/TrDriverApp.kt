package net.neciparmagan.trdriver

import android.app.Application
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.backup.WifiBackupWatcher
import net.neciparmagan.trdriver.data.SessionStore

class TrDriverApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val session = SessionStore(this)
        // Enforce Wi‑Fi-only policy and keep session prefs warm.
        session.wifiOnlyBackup = true
        session.disableCommsBackup()
        WifiBackupWatcher.start(this)
        if (session.anyBackupEnabled() && session.isLoggedIn) {
            // WorkManager only here — starting a foreground service from Application
            // crashes on Android 12+ when the app is not yet visible.
            GalleryBackupWorker.schedule(this)
        }
    }
}
