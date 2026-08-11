package net.neciparmagan.trdriver

import android.app.Application
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.data.SessionStore

class TrDriverApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val session = SessionStore(this)
        if (session.galleryBackupEnabled && session.isLoggedIn) {
            GalleryBackupWorker.schedule(this)
        }
    }
}
