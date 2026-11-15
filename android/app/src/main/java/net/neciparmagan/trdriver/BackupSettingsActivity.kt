package net.neciparmagan.trdriver

import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
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
import net.neciparmagan.trdriver.backup.GalleryBackupEngine
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.backup.OemPowerHelper
import net.neciparmagan.trdriver.backup.TrKeepAliveService
import net.neciparmagan.trdriver.data.BackupPolicy
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.data.UploadedMediaDb

class BackupSettingsActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var switchGallery: SwitchMaterial
    private lateinit var switchPhoto: SwitchMaterial
    private lateinit var switchVideo: SwitchMaterial
    private lateinit var switchAudio: SwitchMaterial
    private lateinit var switchDoc: SwitchMaterial
    private lateinit var switchAllFiles: SwitchMaterial
    private lateinit var backupStatus: TextView
    private lateinit var networkHint: TextView
    private lateinit var folderList: LinearLayout
    private lateinit var presetList: LinearLayout

    /** Guards programmatic setChecked() calls from re-entering listeners. */
    private var uiUpdating = false

    /** Preset the SAF picker was opened for; its path is stored on success. */
    private var pendingPresetPath: String? = null

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

    /** Type-toggle permission requests: only refresh, never flip the master. */
    private val typePermLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { _ ->
            refreshStatus()
            if (MediaAccess.hasMediaAccess(this)) {
                GalleryBackupWorker.schedule(this)
            }
        }

    private val audioPermLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { _ ->
            if (MediaAccess.hasAudioAccess(this)) {
                uiUpdating = true
                switchAudio.isChecked = true
                uiUpdating = false
                session.backupAudioOn = true
                GalleryBackupEngine.invalidateScanCache()
                GalleryBackupWorker.schedule(this)
                Toast.makeText(this, "Ses yedekleme açıldı", Toast.LENGTH_SHORT).show()
            } else {
                uiUpdating = true
                switchAudio.isChecked = false
                uiUpdating = false
                session.backupAudioOn = false
                Toast.makeText(this, "Ses izni gerekli (READ_MEDIA_AUDIO)", Toast.LENGTH_LONG).show()
            }
            refreshStatus()
        }

    private val notificationLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { /* optional */ }

    private val treePicker = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri == null) {
            pendingPresetPath = null
            return@registerForActivityResult
        }
        runCatching {
            contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        session.addBackupFolderUri(uri.toString())
        // Remember the matching direct path too: once all-files access is
        // granted later, the preset is walked directly and this tree is
        // skipped as covered (no duplicate upload).
        pendingPresetPath?.let { session.addBackupPresetPath(it) }
        pendingPresetPath = null
        if (!session.anyBackupEnabled()) {
            Toast.makeText(
                this,
                "Klasör eklendi. Yedeklemek için otomatik yedeği açın.",
                Toast.LENGTH_LONG,
            ).show()
        } else {
            GalleryBackupWorker.schedule(this)
        }
        renderPresets()
        renderFolders()
        refreshStatus()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_backup_settings)
        session = SessionStore(this)
        session.wifiOnlyBackup = true
        session.disableCommsBackup()

        switchGallery = findViewById(R.id.switchGalleryBackup)
        switchPhoto = findViewById(R.id.switchPhotoBackup)
        switchVideo = findViewById(R.id.switchVideoBackup)
        switchAudio = findViewById(R.id.switchAudioBackup)
        switchDoc = findViewById(R.id.switchDocBackup)
        switchAllFiles = findViewById(R.id.switchAllFilesAccess)
        backupStatus = findViewById(R.id.backupStatus)
        networkHint = findViewById(R.id.networkHint)
        folderList = findViewById(R.id.folderList)
        presetList = findViewById(R.id.presetList)

        // Initial state before listeners are attached.
        switchGallery.isChecked = session.galleryBackupEnabled
        switchPhoto.isChecked = session.backupPhotosOn
        switchVideo.isChecked = session.backupVideosOn
        switchAudio.isChecked = session.backupAudioOn
        switchDoc.isChecked = session.backupDocsOn

        switchGallery.setOnCheckedChangeListener { _, checked ->
            if (uiUpdating) return@setOnCheckedChangeListener
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

        switchPhoto.setOnCheckedChangeListener { _, checked ->
            if (uiUpdating) return@setOnCheckedChangeListener
            session.backupPhotosOn = checked
            rescanAfterTypeChange(needsMediaPerms = checked && !MediaAccess.hasMediaAccess(this))
        }
        switchVideo.setOnCheckedChangeListener { _, checked ->
            if (uiUpdating) return@setOnCheckedChangeListener
            session.backupVideosOn = checked
            rescanAfterTypeChange(needsMediaPerms = checked && !MediaAccess.hasMediaAccess(this))
        }
        switchAudio.setOnCheckedChangeListener { _, checked ->
            if (uiUpdating) return@setOnCheckedChangeListener
            if (checked && !MediaAccess.hasAudioAccess(this)) {
                uiUpdating = true
                switchAudio.isChecked = false
                uiUpdating = false
                audioPermLauncher.launch(MediaAccess.audioPermissionsForRequest())
                return@setOnCheckedChangeListener
            }
            session.backupAudioOn = checked
            rescanAfterTypeChange(needsMediaPerms = false)
        }
        switchDoc.setOnCheckedChangeListener { _, checked ->
            if (uiUpdating) return@setOnCheckedChangeListener
            session.backupDocsOn = checked
            rescanAfterTypeChange(needsMediaPerms = false)
        }

        switchAllFiles.setOnCheckedChangeListener { _, checked ->
            if (uiUpdating) return@setOnCheckedChangeListener
            if (checked) {
                if (MediaAccess.hasDirectStorageAccess(this)) {
                    onAllFilesAccessGranted()
                } else if (Build.VERSION.SDK_INT >= 30) {
                    explainAllFilesAccess()
                } else {
                    // Legacy devices: all-files access == READ_EXTERNAL_STORAGE.
                    typePermLauncher.launch(
                        arrayOf(android.Manifest.permission.READ_EXTERNAL_STORAGE),
                    )
                }
            } else {
                if (MediaAccess.hasDirectStorageAccess(this) && Build.VERSION.SDK_INT >= 30) {
                    Toast.makeText(
                        this,
                        "Erişim kaldırılmak için: Ayarlar → Uygulama → İzinler → Tüm dosyalar",
                        Toast.LENGTH_LONG,
                    ).show()
                    uiUpdating = true
                    switchAllFiles.isChecked = true
                    uiUpdating = false
                } else {
                    refreshStatus()
                }
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
                Toast.makeText(this, "Önce otomatik yedeği açın", Toast.LENGTH_SHORT).show()
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
            val toggles = session.backupTypeToggles()
            if (!toggles.photo && !toggles.video && !toggles.audio && !toggles.doc) {
                Toast.makeText(this, "En az bir dosya türü seçin", Toast.LENGTH_LONG).show()
                return@setOnClickListener
            }
            val nothingToScan = !MediaAccess.hasMediaAccess(this) &&
                !MediaAccess.hasAudioAccess(this) &&
                session.backupFolderUris.isEmpty() &&
                session.backupPresetPaths.isEmpty()
            if (nothingToScan) {
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
            pendingPresetPath = null
            treePicker.launch(null)
        }
        findViewById<Button>(R.id.btnCloseBackup).setOnClickListener { finish() }

        renderPresets()
        renderFolders()
        refreshStatus()
    }

    override fun onResume() {
        super.onResume()
        warnPartialAccessIfNeeded()
        syncAllFilesSwitch()
        refreshStatus()
        renderPresets()
        renderFolders()
    }

    private fun rescanAfterTypeChange(needsMediaPerms: Boolean) {
        GalleryBackupEngine.invalidateScanCache()
        if (session.galleryBackupEnabled && session.isLoggedIn) {
            GalleryBackupWorker.schedule(this)
        }
        if (needsMediaPerms) {
            // Refresh-only launcher: never flips the master backup switch.
            typePermLauncher.launch(MediaAccess.mediaPermissionsForRequest())
        }
        refreshStatus()
    }

    private fun explainAllFilesAccess() {
        AlertDialog.Builder(this)
            .setTitle("Tüm dosyalara erişim")
            .setMessage(
                "Seçilen klasörleri (Download, DCIM, WhatsApp yedeği vb.) tam olarak yedeklemek için " +
                    "\"Tüm dosyalara erişim\" izni gerekir. Bu izin yalnızca yedekleme için kullanılır; " +
                    "İzinler ekranından geri alabilirsiniz.\n\nAlternatif: aşağıdaki SAF ile tek tek klasör seçin.",
            )
            .setPositiveButton("İzin ver") { _, _ ->
                session.allFilesAccessAsked = true
                MediaAccess.openAllFilesAccessSettings(this)
            }
            .setNegativeButton("Vazgeç") { _, _ ->
                uiUpdating = true
                switchAllFiles.isChecked = false
                uiUpdating = false
            }
            .setOnCancelListener {
                uiUpdating = true
                switchAllFiles.isChecked = false
                uiUpdating = false
            }
            .show()
    }

    private fun onAllFilesAccessGranted() {
        // First grant: seed the standard folders so backup starts immediately.
        if (session.backupPresetPaths.isEmpty()) {
            for (p in BackupPolicy.presets()) {
                session.addBackupPresetPath(p.path)
            }
        }
        GalleryBackupEngine.invalidateScanCache()
        if (session.anyBackupEnabled() && session.isLoggedIn) {
            GalleryBackupWorker.schedule(this)
        }
        Toast.makeText(this, "Tüm dosyalara erişim açık — hızlı klasörler aktif", Toast.LENGTH_SHORT).show()
        refreshStatus()
    }

    private fun syncAllFilesSwitch() {
        val granted = MediaAccess.hasDirectStorageAccess(this)
        if (switchAllFiles.isChecked != granted) {
            uiUpdating = true
            switchAllFiles.isChecked = granted
            uiUpdating = false
        }
        if (granted && session.backupPresetPaths.isEmpty()) {
            onAllFilesAccessGranted()
        }
    }

    private fun addPreset(preset: BackupPolicy.FolderPreset) {
        if (MediaAccess.hasDirectStorageAccess(this)) {
            session.addBackupPresetPath(preset.path)
            GalleryBackupEngine.invalidateScanCache()
            if (session.anyBackupEnabled() && session.isLoggedIn) {
                GalleryBackupWorker.schedule(this)
            }
            renderPresets()
            renderFolders()
            refreshStatus()
            Toast.makeText(this, "${preset.label} eklendi", Toast.LENGTH_SHORT).show()
            return
        }
        // No direct access: open the system picker pre-seeded at the folder.
        // OpenDocumentTree passes EXTRA_INITIAL_URI through on API 26+.
        val initial = Uri.parse(
            "content://com.android.externalstorage.documents/document/" +
                Uri.encode(preset.treeDocumentId),
        )
        pendingPresetPath = preset.path
        treePicker.launch(initial)
    }

    private fun removePreset(preset: BackupPolicy.FolderPreset) {
        session.removeBackupPresetPath(preset.path)
        for (raw in session.backupFolderUris) {
            if (BackupPolicy.treeUriToPath(raw) == preset.path) {
                runCatching {
                    val uri = Uri.parse(raw)
                    contentResolver.releasePersistableUriPermission(
                        uri,
                        Intent.FLAG_GRANT_READ_URI_PERMISSION,
                    )
                }
                session.removeBackupFolderUri(raw)
            }
        }
        GalleryBackupEngine.invalidateScanCache()
        renderPresets()
        renderFolders()
        refreshStatus()
    }

    private fun renderPresets() {
        presetList.removeAllViews()
        val direct = MediaAccess.hasDirectStorageAccess(this)
        for (preset in BackupPolicy.presets()) {
            val addedDirect = session.backupPresetPaths.contains(preset.path)
            val addedSaf = session.backupFolderUris.any {
                BackupPolicy.treeUriToPath(it) == preset.path
            }
            val added = addedDirect || addedSaf
            val row = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                setPadding(0, 4, 0, 4)
            }
            val label = TextView(this).apply {
                layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
                text = if (added) "✓ ${preset.label}" else preset.label
                setTextColor(ContextCompat.getColor(this@BackupSettingsActivity, R.color.tr_ink))
            }
            val action = Button(this).apply {
                text = if (added) "Kaldır" else "Ekle"
                setOnClickListener {
                    if (added) removePreset(preset) else addPreset(preset)
                }
            }
            row.addView(label)
            row.addView(action)
            presetList.addView(row)
        }
        if (!direct) {
            val hint = TextView(this).apply {
                text = "İpucu: “Tüm dosyalara erişim” açıkken klasörler tek dokunuşla eklenir."
                setTextColor(ContextCompat.getColor(this@BackupSettingsActivity, R.color.tr_ink))
                setPadding(0, 8, 0, 8)
                textSize = 12f
            }
            presetList.addView(hint)
        }
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
        val presetPaths = BackupPolicy.presets().map { it.path }
        // Preset-covered trees are managed by the preset rows above.
        val uris = session.backupFolderUris.filter {
            !BackupPolicy.pathCoveredByPreset(BackupPolicy.treeUriToPath(it), presetPaths)
        }
        if (uris.isEmpty()) {
            val empty = TextView(this).apply {
                text = "Ek klasör yok (yukarıdan hızlı klasör ekleyebilirsiniz)"
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
        val presetCount = session.backupPresetPaths.size
        val covered = BackupPolicy.presets().map { it.path }
        val safCount = session.backupFolderUris.count {
            !BackupPolicy.pathCoveredByPreset(BackupPolicy.treeUriToPath(it), covered)
        }
        val folders = presetCount + safCount
        val last = session.lastBackupMessage.ifBlank { "Henüz çalışmadı" }
        val freeable = UploadedMediaDb(this).countNotFreed()
        val oem = if (OemPowerHelper.isXiaomiFamily()) " · Xiaomi/HyperOS" else ""
        val partial = if (MediaAccess.hasPartialMediaAccess(this)) " · ⚠ sınırlı galeri" else ""
        val wifiNow = if (UploadNetworkGate.isWifi(this)) " · Wi‑Fi bağlı" else " · Wi‑Fi bekleniyor"
        val fgs = if (!session.anyBackupEnabled()) {
            "Arka plan: kapalı"
        } else if (TrKeepAliveService.running) {
            "Arka plan: çalışıyor ✓"
        } else {
            "Arka plan: hazırlanıyor…"
        }
        val heartbeat = session.lastBackupHeartbeatAt
        val heartbeatLine = if (heartbeat > 0) {
            "Son çalışma: ${formatAgo(heartbeat)}"
        } else {
            "Henüz yedek çalışmadı"
        }
        val pending = session.backupPendingCount
        val pendingLine = if (session.anyBackupEnabled()) {
            if (pending > 0) "Bekleyen: $pending dosya" else "Bekleyen dosya yok"
        } else {
            "Bekleyen: — (yedek kapalı)"
        }
        val success = session.lastBackupSuccessAt
        val successLine = if (success > 0) {
            "Son tam yedek: ${formatAgo(success)}"
        } else {
            "Son tam yedek: henüz tamamlanmadı"
        }
        val battery = if (OemPowerHelper.isIgnoringBatteryOptimizations(this)) {
            "Pil: muaf ✓"
        } else {
            "Pil: kısıtlı olabilir ⚠"
        }
        val toggles = session.backupTypeToggles()
        val types = buildString {
            if (toggles.photo) append("Foto ✓ ")
            if (toggles.video) append("Video ✓ ")
            if (toggles.audio) append("Ses ✓ ")
            if (toggles.doc) append("Dosya ✓ ")
            if (!toggles.photo && !toggles.video && !toggles.audio && !toggles.doc) append("tür yok ⚠")
        }
        val allFiles = if (MediaAccess.hasDirectStorageAccess(this)) {
            " · tam erişim ✓"
        } else {
            " · SAF klasörler"
        }
        backupStatus.text =
            "Durum: $on · $net$wifiNow · Türler: $types$allFiles · İşlenen medya: $count · Yer açılabilir: $freeable · Ek klasör: $folders$oem$partial\n$last\n" +
                "Galeri/ses → TR Photos · klasörler → TR Backup / ${session.deviceName}\n" +
                "$fgs · $heartbeatLine · $battery\n$pendingLine · $successLine"
        networkHint.text =
            "Yedekleme yalnız Wi‑Fi ile yapılır (fotoğraf, video, ses ve tüm dosya türleri). " +
                "Artımlı çalışır: değişmeyen dosyalar tekrar yüklenmez."
    }

    private fun formatAgo(atMs: Long): String {
        val diffMin = ((System.currentTimeMillis() - atMs).coerceAtLeast(0L) / 60_000L).toInt()
        return when {
            diffMin < 1 -> "az önce"
            diffMin < 60 -> "$diffMin dk önce"
            diffMin < 60 * 24 -> "${diffMin / 60} sa önce"
            else -> "${diffMin / (60 * 24)} gün önce"
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
