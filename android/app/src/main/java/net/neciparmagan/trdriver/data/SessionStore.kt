package net.neciparmagan.trdriver.data

import android.content.Context
import net.neciparmagan.trdriver.BuildConfig
import org.json.JSONArray

class SessionStore(context: Context) {
    private val prefs = context.getSharedPreferences("trdriver_prefs", Context.MODE_PRIVATE)

    var serverUrl: String
        get() = prefs.getString(KEY_SERVER, BuildConfig.DEFAULT_SERVER)?.trimEnd('/')
            ?: BuildConfig.DEFAULT_SERVER
        set(value) = prefs.edit().putString(KEY_SERVER, value.trimEnd('/')).apply()

    var token: String?
        get() = prefs.getString(KEY_TOKEN, null)
        set(value) = prefs.edit().putString(KEY_TOKEN, value).apply()

    var email: String
        get() = prefs.getString(KEY_EMAIL, "") ?: ""
        set(value) = prefs.edit().putString(KEY_EMAIL, value).apply()

    var storageRootId: String
        get() = prefs.getString(KEY_ROOT, "") ?: ""
        set(value) = prefs.edit().putString(KEY_ROOT, value).apply()

    var photosRootId: String
        get() = prefs.getString(KEY_PHOTOS_ROOT, "") ?: ""
        set(value) = prefs.edit().putString(KEY_PHOTOS_ROOT, value).apply()

    var galleryBackupEnabled: Boolean
        get() = prefs.getBoolean(KEY_GALLERY_ON, false)
        set(value) = prefs.edit().putBoolean(KEY_GALLERY_ON, value).apply()

    /** true = yalnız Wi‑Fi; false = mobil veri de kullanılabilir */
    var wifiOnlyBackup: Boolean
        get() = prefs.getBoolean(KEY_WIFI_ONLY, true)
        set(value) = prefs.edit().putBoolean(KEY_WIFI_ONLY, value).apply()

    var lastBackupMessage: String
        get() = prefs.getString(KEY_LAST_MSG, "") ?: ""
        set(value) = prefs.edit().putString(KEY_LAST_MSG, value).apply()

    var deviceName: String
        get() {
            val stored = prefs.getString(KEY_DEVICE, "") ?: ""
            if (stored.isNotBlank()) return stored
            val generated = "Android-" + (android.os.Build.MODEL ?: "Phone").replace(" ", "-")
            prefs.edit().putString(KEY_DEVICE, generated).apply()
            return generated
        }
        set(value) = prefs.edit().putString(KEY_DEVICE, value).apply()

    /** Persistable SAF tree URIs for extra backup folders. */
    var backupFolderUris: List<String>
        get() {
            val raw = prefs.getString(KEY_BACKUP_FOLDERS, "[]") ?: "[]"
            return runCatching {
                val arr = JSONArray(raw)
                buildList {
                    for (i in 0 until arr.length()) {
                        val s = arr.optString(i)
                        if (s.isNotBlank()) add(s)
                    }
                }
            }.getOrDefault(emptyList())
        }
        set(value) {
            val arr = JSONArray()
            value.distinct().forEach { arr.put(it) }
            prefs.edit().putString(KEY_BACKUP_FOLDERS, arr.toString()).apply()
        }

    fun addBackupFolderUri(uri: String) {
        if (uri.isBlank()) return
        backupFolderUris = backupFolderUris + uri
    }

    fun removeBackupFolderUri(uri: String) {
        backupFolderUris = backupFolderUris.filterNot { it == uri }
    }

    val isLoggedIn: Boolean get() = !token.isNullOrBlank()

    fun clearAuth() {
        prefs.edit()
            .remove(KEY_TOKEN)
            .remove(KEY_ROOT)
            .remove(KEY_PHOTOS_ROOT)
            .apply()
    }

    companion object {
        private const val KEY_SERVER = "server"
        private const val KEY_TOKEN = "token"
        private const val KEY_EMAIL = "email"
        private const val KEY_ROOT = "root"
        private const val KEY_PHOTOS_ROOT = "photos_root"
        private const val KEY_GALLERY_ON = "gallery_on"
        private const val KEY_WIFI_ONLY = "wifi_only"
        private const val KEY_LAST_MSG = "last_backup_msg"
        private const val KEY_DEVICE = "device_name"
        private const val KEY_BACKUP_FOLDERS = "backup_folders"
    }
}
