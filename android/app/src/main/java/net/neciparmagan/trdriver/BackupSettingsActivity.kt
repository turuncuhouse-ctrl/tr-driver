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
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.documentfile.provider.DocumentFile
import com.google.android.material.switchmaterial.SwitchMaterial
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.backup.OemPowerHelper
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.data.UploadedMediaDb

class BackupSettingsActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var switchGallery: SwitchMaterial
    private lateinit var switchBackupWifi: SwitchMaterial
    private lateinit var switchBackupMobile: SwitchMaterial
    private lateinit var backupStatus: TextView
    private lateinit var networkHint: TextView
    private lateinit var folderList: LinearLayout

    private val permissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { _ ->
            if (MediaAccess.hasMediaAccess(this)) {
                session.galleryBackupEnabled = true
                switchGallery.isChecked = true
                GalleryBackupWorker.schedule(this)
                maybeRequestNotifications()
                warnPartialAccessIfNeeded()
                OemPowerHelper.maybePromptForReliableBackup(this)
                refreshStatus()
                Toast.makeText(this, "Galeri yedekleme açıldı", Toast.LENGTH_SHORT).show()
            } else {
                switchGallery.isChecked = false
                session.galleryBackupEnabled = false
                Toast.makeText(this, "Galeri izni gerekli (fotoğraf/video)", Toast.LENGTH_LONG).show()
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
        switchBackupWifi = findViewById(R.id.switchBackupWifi)
        switchBackupMobile = findViewById(R.id.switchBackupMobile)
        backupStatus = findViewById(R.id.backupStatus)
        networkHint = findViewById(R.id.networkHint)
        folderList = findViewById(R.id.folderList)

        switchGallery.isChecked = session.galleryBackupEnabled
        switchBackupWifi.isChecked = session.backupOnWifi
        switchBackupMobile.isChecked = session.backupOnMobile

        switchGallery.setOnCheckedChangeListener { _, checked ->
            if (checked) {
                if (MediaAccess.hasMediaAccess(this)) {
                    session.galleryBackupEnabled = true
                    ensureNetworkEnabled()
                    GalleryBackupWorker.schedule(this)
                    OemPowerHelper.maybePromptForReliableBackup(this)
                    warnPartialAccessIfNeeded()
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
        switchBackupWifi.setOnCheckedChangeListener { _, checked ->
            applyNetworkToggles(wifi = checked, mobile = switchBackupMobile.isChecked)
        }
        switchBackupMobile.setOnCheckedChangeListener { _, checked ->
            applyNetworkToggles(wifi = switchBackupWifi.isChecked, mobile = checked)
        }

        findViewById<Button>(R.id.btnOemSettings).setOnClickListener {
            OemPowerHelper.maybePromptForReliableBackup(this)
        }
        findViewById<Button>(R.id.btnBackupNow).setOnClickListener {
            if (!session.galleryBackupEnabled) {
                Toast.makeText(this, "Önce otomatik yedeklemeyi açın", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            if (!session.backupOnWifi && !session.backupOnMobile) {
                Toast.makeText(this, "En az bir ağ seçeneğini açın", Toast.LENGTH_LONG).show()
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
        warnPartialAccessIfNeeded()
        refreshStatus()
        renderFolders()
    }

    private fun applyNetworkToggles(wifi: Boolean, mobile: Boolean) {
        if (!wifi && !mobile) {
            Toast.makeText(this, "En az Wi‑Fi veya mobil veri açık olmalı", Toast.LENGTH_LONG).show()
            switchBackupWifi.isChecked = true
            switchBackupMobile.isChecked = true
            session.backupOnWifi = true
            session.backupOnMobile = true
        } else {
            session.backupOnWifi = wifi
            session.backupOnMobile = mobile
        }
        if (session.galleryBackupEnabled) {
            GalleryBackupWorker.schedule(this)
        }
        refreshStatus()
    }

    private fun ensureNetworkEnabled() {
        if (!session.backupOnWifi && !session.backupOnMobile) {
            session.backupOnWifi = true
            session.backupOnMobile = true
            switchBackupWifi.isChecked = true
            switchBackupMobile.isChecked = true
        }
    }

    private fun warnPartialAccessIfNeeded() {
        if (!MediaAccess.hasPartialMediaAccess(this)) return
        AlertDialog.Builder(this)
            .setTitle("Sınırlı galeri erişimi")
            .setMessage(
                "Telefon yalnızca seçtiğiniz fotoğraflara izin veriyor. " +
                    "Tüm galeriyi yedeklemek için izinleri \"Tüm fotoğraflar\" olarak güncelleyin. " +
                    "(Note 15 / Android 15'te sık görülür)",
            )
            .setPositiveButton("İzin ayarları") { _, _ -> MediaAccess.openAppSettings(this) }
            .setNegativeButton("Tamam", null)
            .show()
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
        val net = UploadNetworkGate.networkPolicyLabel(session)
        val on = if (session.galleryBackupEnabled) "Açık" else "Kapalı"
        val folders = session.backupFolderUris.size
        val last = session.lastBackupMessage.ifBlank { "Henüz çalışmadı" }
        val freeable = UploadedMediaDb(this).countNotFreed()
        val oem = if (OemPowerHelper.isXiaomiFamily()) " · Xiaomi/HyperOS" else ""
        val partial = if (MediaAccess.hasPartialMediaAccess(this)) " · ⚠ sınırlı galeri" else ""
        backupStatus.text =
            "Durum: $on · $net · İşlenen: $count · Yer açılabilir: $freeable · Ek klasör: $folders$oem$partial\n$last\n" +
                "Galeri → TR Photos · Klasörler → TR Backup / ${session.deviceName}"
        networkHint.text = when {
            session.backupOnWifi && session.backupOnMobile ->
                "Wi‑Fi ve mobil veri açık — çoğu kullanıcı için önerilen ayar."
            session.backupOnMobile -> "Yalnız mobil veri — Wi‑Fi'de yedekleme bekler."
            session.backupOnWifi -> "Yalnız Wi‑Fi — mobil veride yedekleme yapılmaz."
            else -> "Ağ kapalı — yedekleme çalışmaz."
        }
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
