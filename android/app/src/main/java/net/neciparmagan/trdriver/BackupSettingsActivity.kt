package net.neciparmagan.trdriver

import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.documentfile.provider.DocumentFile
import com.google.android.material.switchmaterial.SwitchMaterial
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadedMediaDb

class BackupSettingsActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var switchGallery: SwitchMaterial
    private lateinit var switchWifiOnly: SwitchMaterial
    private lateinit var backupStatus: TextView
    private lateinit var folderList: LinearLayout

    private val permissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { result ->
            // Ignore notification-only grants — media access is what enables backup.
            if (MediaAccess.hasMediaAccess(this)) {
                session.galleryBackupEnabled = true
                switchGallery.isChecked = true
                GalleryBackupWorker.schedule(this)
                maybeRequestNotifications()
                refreshStatus()
                Toast.makeText(this, "Galeri yedekleme açıldı", Toast.LENGTH_SHORT).show()
            } else {
                switchGallery.isChecked = false
                session.galleryBackupEnabled = false
                val anyDenied = result.values.any { !it }
                Toast.makeText(
                    this,
                    if (anyDenied) "Galeri izni gerekli (fotoğraf/video)" else "Galeri izni gerekli",
                    Toast.LENGTH_LONG,
                ).show()
            }
        }

    private val notificationLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { /* optional */ }

    private val treePicker = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri == null) return@registerForActivityResult
        runCatching {
            contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        session.addBackupFolderUri(uri.toString())
        if (!session.galleryBackupEnabled) {
            Toast.makeText(
                this,
                "Klasör eklendi. Yedeklemek için otomatik yedeği açın.",
                Toast.LENGTH_LONG,
            ).show()
        } else {
            GalleryBackupWorker.schedule(this)
        }
        renderFolders()
        refreshStatus()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_backup_settings)
        session = SessionStore(this)

        switchGallery = findViewById(R.id.switchGalleryBackup)
        switchWifiOnly = findViewById(R.id.switchWifiOnly)
        backupStatus = findViewById(R.id.backupStatus)
        folderList = findViewById(R.id.folderList)

        switchGallery.isChecked = session.galleryBackupEnabled
        switchWifiOnly.isChecked = session.wifiOnlyBackup

        switchGallery.setOnCheckedChangeListener { _, checked ->
            if (checked) {
                if (MediaAccess.hasMediaAccess(this)) {
                    session.galleryBackupEnabled = true
                    GalleryBackupWorker.schedule(this)
                    refreshStatus()
                } else {
                    switchGallery.isChecked = false
                    requestMediaPermission()
                }
            } else {
                session.galleryBackupEnabled = false
                GalleryBackupWorker.schedule(this)
                refreshStatus()
            }
        }
        switchWifiOnly.setOnCheckedChangeListener { _, checked ->
            session.wifiOnlyBackup = checked
            if (session.galleryBackupEnabled) {
                GalleryBackupWorker.schedule(this)
            }
            refreshStatus()
        }

        findViewById<Button>(R.id.btnBackupNow).setOnClickListener {
            if (!session.galleryBackupEnabled) {
                Toast.makeText(this, "Önce otomatik yedeklemeyi açın", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            if (!MediaAccess.hasMediaAccess(this) && session.backupFolderUris.isEmpty()) {
                requestMediaPermission()
                return@setOnClickListener
            }
            GalleryBackupWorker.runNow(this)
            Toast.makeText(this, "Yedekleme başlatıldı", Toast.LENGTH_SHORT).show()
            refreshStatus()
        }
        findViewById<Button>(R.id.btnOpenPhotos).setOnClickListener {
            startActivity(Intent(this, PhotosLibraryActivity::class.java))
        }
        findViewById<Button>(R.id.btnFreeUpSpace).setOnClickListener {
            startActivity(Intent(this, FreeUpSpaceActivity::class.java))
        }
        findViewById<Button>(R.id.btnAddFolder).setOnClickListener {
            treePicker.launch(null)
        }
        findViewById<Button>(R.id.btnCloseBackup).setOnClickListener { finish() }

        renderFolders()
        refreshStatus()
    }

    override fun onResume() {
        super.onResume()
        refreshStatus()
        renderFolders()
    }

    private fun renderFolders() {
        folderList.removeAllViews()
        val uris = session.backupFolderUris
        if (uris.isEmpty()) {
            val empty = TextView(this).apply {
                text = "Henüz ek klasör yok"
                setTextColor(ContextCompat.getColor(this@BackupSettingsActivity, R.color.tr_ink))
                setPadding(0, 8, 0, 8)
            }
            folderList.addView(empty)
            return
        }
        for (raw in uris) {
            val uri = Uri.parse(raw)
            val name = DocumentFile.fromTreeUri(this, uri)?.name ?: raw.takeLast(40)
            val row = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                setPadding(0, 6, 0, 6)
            }
            val label = TextView(this).apply {
                layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
                text = name
                setTextColor(ContextCompat.getColor(this@BackupSettingsActivity, R.color.tr_ink))
            }
            val remove = Button(this).apply {
                text = "Kaldır"
                setOnClickListener {
                    runCatching {
                        contentResolver.releasePersistableUriPermission(
                            uri,
                            Intent.FLAG_GRANT_READ_URI_PERMISSION,
                        )
                    }
                    session.removeBackupFolderUri(raw)
                    renderFolders()
                    refreshStatus()
                }
            }
            row.addView(label)
            row.addView(remove)
            folderList.addView(row)
        }
    }

    private fun refreshStatus() {
        val count = UploadedMediaDb(this).countUploaded()
        val net = if (session.wifiOnlyBackup) "yalnız Wi‑Fi" else "Wi‑Fi + mobil veri"
        val on = if (session.galleryBackupEnabled) "Açık" else "Kapalı"
        val folders = session.backupFolderUris.size
        val last = session.lastBackupMessage.ifBlank { "Henüz çalışmadı" }
        val freeable = UploadedMediaDb(this).countNotFreed()
        backupStatus.text =
            "Durum: $on · $net · İşlenen: $count · Yer açılabilir: $freeable · Ek klasör: $folders\n$last\n" +
                "Galeri → TR Photos · Klasörler → TR Backup / ${session.deviceName}"
    }

    private fun mediaPermissions(): Array<String> = MediaAccess.mediaPermissionsForRequest()

    private fun maybeRequestNotifications() {
        val needed = MediaAccess.notificationPermissionOrEmpty().filter {
            ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED
        }
        if (needed.isNotEmpty()) {
            notificationLauncher.launch(needed.toTypedArray())
        }
    }

    private fun requestMediaPermission() {
        permissionLauncher.launch(mediaPermissions())
    }
}
