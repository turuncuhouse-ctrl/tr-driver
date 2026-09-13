package net.neciparmagan.trdriver

import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
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
import net.neciparmagan.trdriver.backup.CommsBackupEngine
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.backup.OemPowerHelper
import net.neciparmagan.trdriver.backup.TrKeepAliveService
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.data.UploadedMediaDb

class BackupSettingsActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var switchGallery: SwitchMaterial
    private lateinit var switchSms: SwitchMaterial
    private lateinit var switchCallLog: SwitchMaterial
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
                Toast.makeText(this, "Galeri yedekleme açıldı (yalnız Wi‑Fi)", Toast.LENGTH_SHORT).show()
            } else {
                switchGallery.isChecked = false
                session.galleryBackupEnabled = false
                Toast.makeText(this, "Galeri izni gerekli (fotoğraf/video)", Toast.LENGTH_LONG).show()
            }
        }

    private val smsPermissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) {
                session.smsBackupEnabled = true
                switchSms.isChecked = true
                GalleryBackupWorker.schedule(this)
                refreshStatus()
                Toast.makeText(this, "SMS yedekleme açıldı", Toast.LENGTH_SHORT).show()
            } else {
                switchSms.isChecked = false
                session.smsBackupEnabled = false
                Toast.makeText(this, "SMS okuma izni gerekli", Toast.LENGTH_LONG).show()
            }
        }

    private val callPermissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) {
                session.callLogBackupEnabled = true
                switchCallLog.isChecked = true
                GalleryBackupWorker.schedule(this)
                refreshStatus()
                Toast.makeText(this, "Arama kaydı yedekleme açıldı", Toast.LENGTH_SHORT).show()
            } else {
                switchCallLog.isChecked = false
                session.callLogBackupEnabled = false
                Toast.makeText(this, "Arama kaydı izni gerekli", Toast.LENGTH_LONG).show()
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
        if (!session.anyBackupEnabled()) {
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
        // Enforce Wi‑Fi-only policy on prefs (clears any old mobile-on setting).
        session.wifiOnlyBackup = true

        switchGallery = findViewById(R.id.switchGalleryBackup)
        switchSms = findViewById(R.id.switchSmsBackup)
        switchCallLog = findViewById(R.id.switchCallLogBackup)
        backupStatus = findViewById(R.id.backupStatus)
        networkHint = findViewById(R.id.networkHint)
        folderList = findViewById(R.id.folderList)

        switchGallery.isChecked = session.galleryBackupEnabled
        switchSms.isChecked = session.smsBackupEnabled
        switchCallLog.isChecked = session.callLogBackupEnabled

        switchGallery.setOnCheckedChangeListener { _, checked ->
            if (checked) {
                if (MediaAccess.hasMediaAccess(this)) {
                    session.galleryBackupEnabled = true
                    GalleryBackupWorker.schedule(this)
                    TrKeepAliveService.startIfNeeded(this)
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
                if (!session.anyBackupEnabled()) TrKeepAliveService.stop(this)
                refreshStatus()
            }
        }

        switchSms.setOnCheckedChangeListener { _, checked ->
            if (checked) {
                if (CommsBackupEngine.hasSmsPermission(this)) {
                    session.smsBackupEnabled = true
                    GalleryBackupWorker.schedule(this)
                    TrKeepAliveService.startIfNeeded(this)
                    refreshStatus()
                } else {
                    switchSms.isChecked = false
                    smsPermissionLauncher.launch(android.Manifest.permission.READ_SMS)
                }
            } else {
                session.smsBackupEnabled = false
                GalleryBackupWorker.schedule(this)
                if (!session.anyBackupEnabled()) TrKeepAliveService.stop(this)
                refreshStatus()
            }
        }

        switchCallLog.setOnCheckedChangeListener { _, checked ->
            if (checked) {
                if (CommsBackupEngine.hasCallLogPermission(this)) {
                    session.callLogBackupEnabled = true
                    GalleryBackupWorker.schedule(this)
                    TrKeepAliveService.startIfNeeded(this)
                    refreshStatus()
                } else {
                    switchCallLog.isChecked = false
                    callPermissionLauncher.launch(android.Manifest.permission.READ_CALL_LOG)
                }
            } else {
                session.callLogBackupEnabled = false
                GalleryBackupWorker.schedule(this)
                if (!session.anyBackupEnabled()) TrKeepAliveService.stop(this)
                refreshStatus()
            }
        }

        findViewById<Button>(R.id.btnOemSettings).setOnClickListener {
            OemPowerHelper.maybePromptForReliableBackup(this)
        }
        findViewById<Button>(R.id.btnBackupNow).setOnClickListener {
            if (!session.isLoggedIn) {
                Toast.makeText(this, "Önce giriş yapın", Toast.LENGTH_LONG).show()
                return@setOnClickListener
            }
            if (!session.anyBackupEnabled()) {
                Toast.makeText(this, "Önce galeri, SMS veya arama yedeğini açın", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            if (!UploadNetworkGate.isWifi(this)) {
                Toast.makeText(
                    this,
                    "Wi‑Fi gerekli. Mobil veri ile yedekleme kapalı; Wi‑Fi’ye bağlanınca devam eder.",
                    Toast.LENGTH_LONG,
                ).show()
                return@setOnClickListener
            }
            if (session.galleryBackupEnabled &&
                !MediaAccess.hasMediaAccess(this) &&
                session.backupFolderUris.isEmpty()
            ) {
                requestMediaPermission()
                return@setOnClickListener
            }
            try {
                GalleryBackupWorker.runNow(this)
                Toast.makeText(this, "Wi‑Fi yedekleme başlatıldı", Toast.LENGTH_SHORT).show()
            } catch (e: Exception) {
                Toast.makeText(this, "Yedek başlatılamadı: ${e.message}", Toast.LENGTH_LONG).show()
            }
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

    private fun warnPartialAccessIfNeeded() {
        if (!MediaAccess.hasPartialMediaAccess(this)) return
        AlertDialog.Builder(this)
            .setTitle("Sınırlı galeri erişimi")
            .setMessage(
                "Telefon yalnızca seçtiğiniz fotoğraflara izin veriyor. " +
                    "Tüm galeriyi yedeklemek için izinleri \"Tüm fotoğraflar\" olarak güncelleyin.",
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
        val on = if (session.anyBackupEnabled()) "Açık" else "Kapalı"
        val sms = if (session.smsBackupEnabled) "SMS:açık" else "SMS:kapalı"
        val calls = if (session.callLogBackupEnabled) "Arama:açık" else "Arama:kapalı"
        val folders = session.backupFolderUris.size
        val last = session.lastBackupMessage.ifBlank { "Henüz çalışmadı" }
        val freeable = UploadedMediaDb(this).countNotFreed()
        val oem = if (OemPowerHelper.isXiaomiFamily()) " · Xiaomi/HyperOS" else ""
        val partial = if (MediaAccess.hasPartialMediaAccess(this)) " · ⚠ sınırlı galeri" else ""
        val wifiNow = if (UploadNetworkGate.isWifi(this)) " · Wi‑Fi bağlı" else " · Wi‑Fi bekleniyor"
        backupStatus.text =
            "Durum: $on · $sms · $calls · $net$wifiNow · İşlenen medya: $count · Yer açılabilir: $freeable · Ek klasör: $folders$oem$partial\n$last\n" +
                "Galeri → TR Photos · SMS/Arama → TR Backup / ${session.deviceName}"
        networkHint.text =
            "Yedekleme yalnız Wi‑Fi. SMS ve arama kayıtları cihazın kendi geçmişidir. WhatsApp/Telegram okunamaz."
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
