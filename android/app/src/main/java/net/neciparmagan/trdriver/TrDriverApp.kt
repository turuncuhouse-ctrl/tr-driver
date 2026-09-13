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
        WifiBackupWatcher.start(this)
        if (session.anyBackupEnabled() && session.isLoggedIn) {
            GalleryBackupWorker.schedule(this)
        }
    }
}
