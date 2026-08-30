package net.neciparmagan.trdriver.data

import android.content.Context
import android.content.SharedPreferences
import net.neciparmagan.trdriver.BuildConfig
import org.json.JSONArray

class SessionStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

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

    /** true = grid (Windows-like thumbs), false = list */
    var filesGridLayout: Boolean
        get() = prefs.getBoolean(KEY_FILES_GRID, false)
        set(value) = prefs.edit().putBoolean(KEY_FILES_GRID, value).apply()

    /** Last folder the user browsed (for share-import target). */
    var lastBrowseFolderId: String
        get() = prefs.getString(KEY_LAST_BROWSE_FOLDER, "") ?: ""
        set(value) = prefs.edit().putString(KEY_LAST_BROWSE_FOLDER, value).apply()

    var galleryBackupEnabled: Boolean
        get() = prefs.getBoolean(KEY_GALLERY_ON, false)
        set(value) = prefs.edit().putBoolean(KEY_GALLERY_ON, value).apply()

    /** Wi‑Fi ağında otomatik yedekleme ve yükleme */
    var backupOnWifi: Boolean
        get() = ensureNetworkPrefsMigrated() && prefs.getBoolean(KEY_BACKUP_WIFI, true)
        set(value) {
            ensureNetworkPrefsMigrated()
            prefs.edit().putBoolean(KEY_BACKUP_WIFI, value).apply()
        }

    /** Mobil veride otomatik yedekleme ve yükleme */
    var backupOnMobile: Boolean
        get() = ensureNetworkPrefsMigrated() && prefs.getBoolean(KEY_BACKUP_MOBILE, true)
        set(value) {
            ensureNetworkPrefsMigrated()
            prefs.edit().putBoolean(KEY_BACKUP_MOBILE, value).apply()
        }

    /** @deprecated Eski tek anahtar; yeni kurulumlarda [backupOnWifi]/[backupOnMobile] kullanın */
    var wifiOnlyBackup: Boolean
        get() = backupOnWifi && !backupOnMobile
        set(value) {
            if (value) {
                backupOnWifi = true
                backupOnMobile = false
            } else {
                backupOnWifi = true
                backupOnMobile = true
            }
        }

    private fun ensureNetworkPrefsMigrated(): Boolean {
        if (prefs.contains(KEY_BACKUP_WIFI) || prefs.contains(KEY_BACKUP_MOBILE)) return true
        val legacyWifiOnly = prefs.getBoolean(KEY_WIFI_ONLY, false)
        prefs.edit()
            .putBoolean(KEY_BACKUP_WIFI, true)
            .putBoolean(KEY_BACKUP_MOBILE, !legacyWifiOnly)
            .apply()
        return true
    }

    var lastBackupMessage: String
        get() = prefs.getString(KEY_LAST_MSG, "") ?: ""
        set(value) = prefs.edit().putString(KEY_LAST_MSG, value).apply()

    var backupActive: Boolean
        get() = prefs.getBoolean(KEY_BACKUP_ACTIVE, false)
        set(value) = prefs.edit().putBoolean(KEY_BACKUP_ACTIVE, value).apply()

    var backupCurrentFile: String
        get() = prefs.getString(KEY_BACKUP_CURRENT, "") ?: ""
        set(value) = prefs.edit().putString(KEY_BACKUP_CURRENT, value).apply()

    var backupDoneCount: Int
        get() = prefs.getInt(KEY_BACKUP_DONE, 0)
        set(value) = prefs.edit().putInt(KEY_BACKUP_DONE, value).apply()

    var backupPendingCount: Int
        get() = prefs.getInt(KEY_BACKUP_PENDING, 0)
        set(value) = prefs.edit().putInt(KEY_BACKUP_PENDING, value).apply()

    var backupPercent: Int
        get() = prefs.getInt(KEY_BACKUP_PERCENT, 0)
        set(value) = prefs.edit().putInt(KEY_BACKUP_PERCENT, value.coerceIn(0, 100)).apply()

    /** Bytes sent for the file currently being uploaded (0 if idle). */
    var backupFileBytesSent: Long
        get() = prefs.getLong(KEY_BACKUP_BYTES_SENT, 0L)
        set(value) = prefs.edit().putLong(KEY_BACKUP_BYTES_SENT, value.coerceAtLeast(0L)).apply()

    var backupFileBytesTotal: Long
        get() = prefs.getLong(KEY_BACKUP_BYTES_TOTAL, 0L)
        set(value) = prefs.edit().putLong(KEY_BACKUP_BYTES_TOTAL, value.coerceAtLeast(0L)).apply()

    /** 0–100 for the current file; falls back to overall backupPercent when unknown. */
    val backupFilePercent: Int
        get() {
            val total = backupFileBytesTotal
            if (total <= 0L) return 0
            return ((backupFileBytesSent * 100L) / total).toInt().coerceIn(0, 100)
        }

    /** Prefer current-file byte progress while an upload is active. */
    val backupDisplayPercent: Int
        get() = if (backupActive && backupFileBytesTotal > 0L) backupFilePercent else backupPercent

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

    /** Last plate folder used in vehicle intake (for quick re-open). */
    var lastIntakePlate: String
        get() = prefs.getString(KEY_LAST_INTAKE_PLATE, "") ?: ""
        set(value) = prefs.edit().putString(KEY_LAST_INTAKE_PLATE, value.trim()).apply()

    var lastIntakeFolderId: String
        get() = prefs.getString(KEY_LAST_INTAKE_FOLDER_ID, "") ?: ""
        set(value) = prefs.edit().putString(KEY_LAST_INTAKE_FOLDER_ID, value).apply()

    /** Recently used intake plates, newest first (max 8). */
    var recentIntakePlates: List<String>
        get() {
            val raw = prefs.getString(KEY_RECENT_INTAKE_PLATES, "[]") ?: "[]"
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
            value.map { it.trim() }.filter { it.isNotBlank() }.distinctBy { it.uppercase() }.take(8)
                .forEach { arr.put(it) }
            prefs.edit().putString(KEY_RECENT_INTAKE_PLATES, arr.toString()).apply()
        }

    fun rememberIntakePlate(plate: String, folderId: String) {
        val p = plate.trim()
        if (p.isBlank() || folderId.isBlank()) return
        lastIntakePlate = p
        lastIntakeFolderId = folderId
        recentIntakePlates = listOf(p) + recentIntakePlates.filterNot { it.equals(p, ignoreCase = true) }
    }

    fun updateBackupProgress(
        active: Boolean,
        currentFile: String = "",
        doneCount: Int = 0,
        pendingCount: Int = 0,
        message: String? = null,
        clearFileBytes: Boolean = false,
    ) {
        val total = doneCount + pendingCount
        val percent = when {
            total <= 0 -> 0
            else -> ((doneCount * 100) / total).coerceIn(0, 100)
        }
        val editor = prefs.edit()
            .putBoolean(KEY_BACKUP_ACTIVE, active)
            .putString(KEY_BACKUP_CURRENT, currentFile)
            .putInt(KEY_BACKUP_DONE, doneCount.coerceAtLeast(0))
            .putInt(KEY_BACKUP_PENDING, pendingCount.coerceAtLeast(0))
            .putInt(KEY_BACKUP_PERCENT, percent)
        if (clearFileBytes || !active) {
            editor.putLong(KEY_BACKUP_BYTES_SENT, 0L).putLong(KEY_BACKUP_BYTES_TOTAL, 0L)
        }
        if (message != null) {
            editor.putString(KEY_LAST_MSG, message)
        }
        editor.apply()
    }

    fun updateBackupFileBytes(sent: Long, total: Long) {
        prefs.edit()
            .putLong(KEY_BACKUP_BYTES_SENT, sent.coerceAtLeast(0L))
            .putLong(KEY_BACKUP_BYTES_TOTAL, total.coerceAtLeast(0L))
            .apply()
    }

    fun clearBackupProgress(message: String? = null) {
        updateBackupProgress(
            active = false,
            currentFile = "",
            doneCount = 0,
            pendingCount = 0,
            message = message,
            clearFileBytes = true,
        )
    }

    var uploadQueueActive: Boolean
        get() = prefs.getBoolean(KEY_UPLOAD_Q_ACTIVE, false)
        set(value) = prefs.edit().putBoolean(KEY_UPLOAD_Q_ACTIVE, value).apply()

    var uploadQueueCurrentFile: String
        get() = prefs.getString(KEY_UPLOAD_Q_CURRENT, "") ?: ""
        set(value) = prefs.edit().putString(KEY_UPLOAD_Q_CURRENT, value).apply()

    var uploadQueueDone: Int
        get() = prefs.getInt(KEY_UPLOAD_Q_DONE, 0)
        set(value) = prefs.edit().putInt(KEY_UPLOAD_Q_DONE, value.coerceAtLeast(0)).apply()

    var uploadQueuePending: Int
        get() = prefs.getInt(KEY_UPLOAD_Q_PENDING, 0)
        set(value) = prefs.edit().putInt(KEY_UPLOAD_Q_PENDING, value.coerceAtLeast(0)).apply()

    var uploadQueueMessage: String
        get() = prefs.getString(KEY_UPLOAD_Q_MSG, "") ?: ""
        set(value) = prefs.edit().putString(KEY_UPLOAD_Q_MSG, value).apply()

    var uploadQueueBytesSent: Long
        get() = prefs.getLong(KEY_UPLOAD_Q_BYTES_SENT, 0L)
        set(value) = prefs.edit().putLong(KEY_UPLOAD_Q_BYTES_SENT, value.coerceAtLeast(0L)).apply()

    var uploadQueueBytesTotal: Long
        get() = prefs.getLong(KEY_UPLOAD_Q_BYTES_TOTAL, 0L)
        set(value) = prefs.edit().putLong(KEY_UPLOAD_Q_BYTES_TOTAL, value.coerceAtLeast(0L)).apply()

    fun updateUploadQueueProgress(
        active: Boolean,
        currentFile: String = "",
        done: Int = 0,
        pending: Int = 0,
        message: String? = null,
        clearBytes: Boolean = false,
    ) {
        val editor = prefs.edit()
            .putBoolean(KEY_UPLOAD_Q_ACTIVE, active)
            .putString(KEY_UPLOAD_Q_CURRENT, currentFile)
            .putInt(KEY_UPLOAD_Q_DONE, done.coerceAtLeast(0))
            .putInt(KEY_UPLOAD_Q_PENDING, pending.coerceAtLeast(0))
        if (message != null) editor.putString(KEY_UPLOAD_Q_MSG, message)
        if (clearBytes || !active) {
            editor.putLong(KEY_UPLOAD_Q_BYTES_SENT, 0L).putLong(KEY_UPLOAD_Q_BYTES_TOTAL, 0L)
        }
        editor.apply()
    }

    fun updateUploadQueueFileBytes(sent: Long, total: Long) {
        prefs.edit()
            .putLong(KEY_UPLOAD_Q_BYTES_SENT, sent.coerceAtLeast(0L))
            .putLong(KEY_UPLOAD_Q_BYTES_TOTAL, total.coerceAtLeast(0L))
            .apply()
    }

    fun clearUploadQueueProgress(message: String? = null) {
        updateUploadQueueProgress(
            active = false,
            currentFile = "",
            done = 0,
            pending = 0,
            message = message,
            clearBytes = true,
        )
    }

    fun backupFileBytesLabel(): String {
        val total = backupFileBytesTotal
        if (total <= 0L) return ""
        return "${formatBytes(backupFileBytesSent)} / ${formatBytes(total)}"
    }

    fun registerBackupListener(listener: SharedPreferences.OnSharedPreferenceChangeListener) {
        prefs.registerOnSharedPreferenceChangeListener(listener)
    }

    fun unregisterBackupListener(listener: SharedPreferences.OnSharedPreferenceChangeListener) {
        prefs.unregisterOnSharedPreferenceChangeListener(listener)
    }

    fun backupStatusLine(): String {
        if (!galleryBackupEnabled) return "Yedek: kapalı"
        if (backupActive) {
            val name = backupCurrentFile.ifBlank { "dosya" }
            val left = backupPendingCount
            val bytes = backupFileBytesLabel()
            val pct = backupDisplayPercent
            return buildString {
                append("Yedekleniyor · $name")
                if (bytes.isNotBlank()) append(" · $bytes")
                append(" · %$pct")
                if (left > 0) append(" · kalan $left")
            }
        }
        if (backupPendingCount > 0) {
            return "Yedek: bekliyor · kalan $backupPendingCount · %$backupPercent"
        }
        val last = lastBackupMessage.ifBlank { "hazır" }
        return "Yedek: açık · $last"
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
        const val PREFS_NAME = "trdriver_prefs"
        const val KEY_BACKUP_ACTIVE = "backup_active"
        const val KEY_BACKUP_CURRENT = "backup_current"
        const val KEY_BACKUP_DONE = "backup_done"
        const val KEY_BACKUP_PENDING = "backup_pending"
        const val KEY_BACKUP_PERCENT = "backup_percent"
        const val KEY_BACKUP_BYTES_SENT = "backup_bytes_sent"
        const val KEY_BACKUP_BYTES_TOTAL = "backup_bytes_total"
        const val KEY_LAST_MSG = "last_backup_msg"
        const val KEY_GALLERY_ON = "gallery_on"

        fun formatBytes(bytes: Long): String {
            if (bytes < 1024) return "$bytes B"
            val kb = bytes / 1024.0
            if (kb < 1024) return String.format("%.1f KB", kb)
            val mb = kb / 1024.0
            if (mb < 1024) return String.format("%.1f MB", mb)
            return String.format("%.2f GB", mb / 1024.0)
        }

        private const val KEY_SERVER = "server"
        private const val KEY_TOKEN = "token"
        private const val KEY_EMAIL = "email"
        private const val KEY_ROOT = "root"
        private const val KEY_PHOTOS_ROOT = "photos_root"
        private const val KEY_WIFI_ONLY = "wifi_only"
        private const val KEY_BACKUP_WIFI = "backup_on_wifi"
        private const val KEY_BACKUP_MOBILE = "backup_on_mobile"
        private const val KEY_DEVICE = "device_name"
        private const val KEY_BACKUP_FOLDERS = "backup_folders"
        private const val KEY_LAST_INTAKE_PLATE = "last_intake_plate"
        private const val KEY_LAST_INTAKE_FOLDER_ID = "last_intake_folder_id"
        private const val KEY_RECENT_INTAKE_PLATES = "recent_intake_plates"
        private const val KEY_FILES_GRID = "files_grid_layout"
        private const val KEY_LAST_BROWSE_FOLDER = "last_browse_folder"
        private const val KEY_UPLOAD_Q_ACTIVE = "upload_q_active"
        private const val KEY_UPLOAD_Q_CURRENT = "upload_q_current"
        private const val KEY_UPLOAD_Q_DONE = "upload_q_done"
        private const val KEY_UPLOAD_Q_PENDING = "upload_q_pending"
        private const val KEY_UPLOAD_Q_MSG = "upload_q_msg"
        private const val KEY_UPLOAD_Q_BYTES_SENT = "upload_q_bytes_sent"
        private const val KEY_UPLOAD_Q_BYTES_TOTAL = "upload_q_bytes_total"
    }
}
