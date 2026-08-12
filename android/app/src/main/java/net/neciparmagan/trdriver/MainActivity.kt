package net.neciparmagan.trdriver

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.MimeTypeMap
import android.widget.Button
import android.widget.EditText
import android.widget.ImageView
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import coil.load
import com.google.android.material.switchmaterial.SwitchMaterial
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadedMediaDb
import net.neciparmagan.trdriver.presentation.DriveViewModel
import okhttp3.Headers
import org.json.JSONObject
import java.io.File

class MainActivity : AppCompatActivity() {
    private val vm: DriveViewModel by viewModels()
    private lateinit var session: SessionStore

    private lateinit var loginPanel: View
    private lateinit var filesPanel: View
    private lateinit var progress: ProgressBar
    private lateinit var crumbs: TextView
    private lateinit var list: RecyclerView
    private lateinit var titleEmail: TextView
    private lateinit var switchGallery: SwitchMaterial
    private lateinit var switchWifiOnly: SwitchMaterial
    private lateinit var backupStatus: TextView
    private lateinit var inputDisplayName: EditText
    private lateinit var btnRegister: Button

    private var registerNameVisible = false

    private val adapter = FileAdapter(
        onOpen = { entry ->
            if (vm.state.value.selectionMode) {
                vm.toggleSelection(entry.id)
            } else {
                openEntry(entry)
            }
        },
        onLongPress = { entry -> vm.enterSelection(entry.id) },
        onDownload = { vm.download(it) },
        onDelete = { entry ->
            AlertDialog.Builder(this)
                .setMessage("\"${entry.name}\" silinsin mi?")
                .setPositiveButton("Sil") { _, _ -> vm.deleteEntry(entry) }
                .setNegativeButton("İptal", null)
                .show()
        },
        onShare = { vm.shareEntry(it) },
        onStar = { vm.toggleStar(it) },
        onToggleCheck = { vm.toggleSelection(it.id) },
    )

    private val picker = registerForActivityResult(ActivityResultContracts.GetContent()) { uri: Uri? ->
        if (uri != null) vm.upload(uri)
    }

    private val qrLauncher = registerForActivityResult(ScanContract()) { result ->
        val contents = result.contents ?: return@registerForActivityResult
        runCatching {
            val json = JSONObject(contents)
            val token = json.optString("challengeToken").ifBlank {
                json.optString("challenge_token")
            }
            val server = json.optString("server").takeIf { it.isNotBlank() }
            if (token.isBlank()) {
                Toast.makeText(this, "QR kodunda challengeToken yok", Toast.LENGTH_LONG).show()
                return@registerForActivityResult
            }
            vm.redeemQr(token, server)
        }.onFailure {
            Toast.makeText(this, "QR okunamadı: ${it.message}", Toast.LENGTH_LONG).show()
        }
    }

    private val permissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { result ->
            val granted = result.values.any { it }
            if (granted) {
                session.galleryBackupEnabled = true
                switchGallery.isChecked = true
                GalleryBackupWorker.schedule(this)
                refreshBackupStatus()
                Toast.makeText(this, "Galeri yedekleme açıldı", Toast.LENGTH_SHORT).show()
            } else {
                switchGallery.isChecked = false
                session.galleryBackupEnabled = false
                Toast.makeText(this, "Galeri izni gerekli", Toast.LENGTH_LONG).show()
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        session = SessionStore(this)

        loginPanel = findViewById(R.id.loginPanel)
        filesPanel = findViewById(R.id.filesPanel)
        progress = findViewById(R.id.progress)
        crumbs = findViewById(R.id.crumbs)
        list = findViewById(R.id.fileList)
        titleEmail = findViewById(R.id.titleEmail)
        switchGallery = findViewById(R.id.switchGalleryBackup)
        switchWifiOnly = findViewById(R.id.switchWifiOnly)
        backupStatus = findViewById(R.id.backupStatus)
        inputDisplayName = findViewById(R.id.inputDisplayName)
        btnRegister = findViewById(R.id.btnRegister)
        list.layoutManager = LinearLayoutManager(this)
        list.adapter = adapter

        val server = findViewById<EditText>(R.id.inputServer)
        val email = findViewById<EditText>(R.id.inputEmail)
        val password = findViewById<EditText>(R.id.inputPassword)
        val otp = findViewById<EditText>(R.id.inputOtp)
        val otpLabel = findViewById<TextView>(R.id.otpLabel)

        findViewById<Button>(R.id.btnLogin).setOnClickListener {
            vm.updateServer(server.text.toString())
            vm.updateEmail(email.text.toString())
            vm.updatePassword(password.text.toString())
            vm.updateOtp(otp.text.toString())
            vm.login()
        }

        btnRegister.setOnClickListener {
            if (!registerNameVisible) {
                registerNameVisible = true
                inputDisplayName.visibility = View.VISIBLE
                btnRegister.text = "Hesabı oluştur"
                inputDisplayName.requestFocus()
                return@setOnClickListener
            }
            val name = inputDisplayName.text.toString().trim()
            val mail = email.text.toString().trim()
            val pass = password.text.toString()
            if (name.isEmpty() || mail.isEmpty() || pass.isEmpty()) {
                Toast.makeText(this, "Ad, e-posta ve şifre gerekli", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            vm.updateServer(server.text.toString())
            vm.updateEmail(mail)
            vm.updatePassword(pass)
            vm.updateDisplayName(name)
            vm.register()
        }

        findViewById<Button>(R.id.btnQrLogin).setOnClickListener {
            val options = ScanOptions().apply {
                setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                setPrompt("TR Driver QR kodunu okutun")
                setBeepEnabled(false)
                setOrientationLocked(true)
            }
            qrLauncher.launch(options)
        }

        findViewById<Button>(R.id.btnRefresh).setOnClickListener { vm.loadFiles() }
        findViewById<Button>(R.id.btnUpload).setOnClickListener { picker.launch("*/*") }
        findViewById<Button>(R.id.btnSearch).setOnClickListener {
            val q = findViewById<EditText>(R.id.inputSearch).text.toString()
            vm.search(q)
        }
        findViewById<EditText>(R.id.inputSearch).setOnEditorActionListener { _, _, _ ->
            vm.search(findViewById<EditText>(R.id.inputSearch).text.toString())
            true
        }
        findViewById<Button>(R.id.btnStarred).setOnClickListener { vm.showStarred() }
        findViewById<Button>(R.id.btnRecent).setOnClickListener { vm.showRecent() }
        findViewById<Button>(R.id.btnOffline).setOnClickListener { showOfflineDownloads() }
        findViewById<Button>(R.id.btnSelectMode).setOnClickListener {
            val first = vm.state.value.files.firstOrNull()
            if (first != null) vm.enterSelection(first.id)
            else Toast.makeText(this, "Önce dosya listesi gerekli", Toast.LENGTH_SHORT).show()
        }
        findViewById<Button>(R.id.btnCancelSelect).setOnClickListener { vm.clearSelection() }
        findViewById<Button>(R.id.btnDeleteSelected).setOnClickListener {
            AlertDialog.Builder(this)
                .setMessage("Seçili öğeler silinsin mi?")
                .setPositiveButton("Sil") { _, _ -> vm.deleteSelected() }
                .setNegativeButton("İptal", null)
                .show()
        }
        findViewById<Button>(R.id.btnDownloadSelected).setOnClickListener { vm.downloadSelected() }
        findViewById<Button>(R.id.btnShareSelected).setOnClickListener {
            val id = vm.state.value.selectedIds.firstOrNull()
            val entry = vm.state.value.files.firstOrNull { it.id == id }
            if (entry == null) {
                Toast.makeText(this, "Paylaşmak için bir öğe seçin", Toast.LENGTH_SHORT).show()
            } else {
                vm.shareEntry(entry)
            }
        }
        findViewById<Button>(R.id.btnNewFolder).setOnClickListener {
            val input = EditText(this).apply { hint = "Klasör adı" }
            AlertDialog.Builder(this)
                .setTitle("Yeni klasör")
                .setView(input)
                .setPositiveButton("Oluştur") { _, _ ->
                    val name = input.text.toString().trim()
                    if (name.isNotEmpty()) vm.createFolder(name)
                }
                .setNegativeButton("İptal", null)
                .show()
        }
        findViewById<Button>(R.id.btnLogout).setOnClickListener {
            session.galleryBackupEnabled = false
            GalleryBackupWorker.schedule(this)
            registerNameVisible = false
            inputDisplayName.visibility = View.GONE
            btnRegister.text = "Üye ol"
            vm.logout()
        }
        crumbs.setOnClickListener {
            val state = vm.state.value
            if (state.crumbs.size > 1) vm.goToCrumb(state.crumbs.lastIndex - 1)
        }

        switchGallery.isChecked = session.galleryBackupEnabled
        switchWifiOnly.isChecked = session.wifiOnlyBackup
        switchGallery.setOnCheckedChangeListener { _, checked ->
            if (checked) {
                if (hasMediaPermission()) {
                    session.galleryBackupEnabled = true
                    GalleryBackupWorker.schedule(this)
                    refreshBackupStatus()
                } else {
                    switchGallery.isChecked = false
                    requestMediaPermission()
                }
            } else {
                session.galleryBackupEnabled = false
                GalleryBackupWorker.schedule(this)
                refreshBackupStatus()
            }
        }
        switchWifiOnly.setOnCheckedChangeListener { _, checked ->
            session.wifiOnlyBackup = checked
            if (session.galleryBackupEnabled) {
                GalleryBackupWorker.schedule(this)
            }
            refreshBackupStatus()
        }
        findViewById<Button>(R.id.btnBackupNow).setOnClickListener {
            if (!session.galleryBackupEnabled) {
                Toast.makeText(this, "Önce otomatik yedeklemeyi açın", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            if (!hasMediaPermission()) {
                requestMediaPermission()
                return@setOnClickListener
            }
            GalleryBackupWorker.runNow(this)
            Toast.makeText(this, "Yedekleme başlatıldı", Toast.LENGTH_SHORT).show()
            refreshBackupStatus()
        }

        lifecycleScope.launch {
            vm.state.collectLatest { state ->
                progress.visibility = if (state.busy) View.VISIBLE else View.GONE
                if (!state.bootstrapped) return@collectLatest

                adapter.updateAuth(session.token.orEmpty(), session.serverUrl)
                adapter.updatePathHint(state.crumbs.joinToString("/") { it.name })

                if (!state.loggedIn) {
                    loginPanel.visibility = View.VISIBLE
                    filesPanel.visibility = View.GONE
                    server.setText(state.serverUrl)
                    email.setText(state.email)
                    otpLabel.visibility = if (state.requires2FA) View.VISIBLE else View.GONE
                    otp.visibility = if (state.requires2FA) View.VISIBLE else View.GONE
                } else {
                    loginPanel.visibility = View.GONE
                    filesPanel.visibility = View.VISIBLE
                    titleEmail.text = state.user?.email ?: state.email
                    crumbs.text = state.crumbs.joinToString(" / ") { it.name }
                    adapter.submit(state.files, state.selectionMode, state.selectedIds)
                    findViewById<View>(R.id.selectionBar).visibility =
                        if (state.selectionMode) View.VISIBLE else View.GONE
                    findViewById<TextView>(R.id.selectionCount).text = "${state.selectedIds.size} seçili"
                    findViewById<View>(R.id.offlineBanner).visibility =
                        if (state.offline) View.VISIBLE else View.GONE
                    if (session.galleryBackupEnabled) {
                        GalleryBackupWorker.schedule(this@MainActivity)
                    }
                    refreshBackupStatus()
                }

                state.message?.let {
                    Toast.makeText(this@MainActivity, it, Toast.LENGTH_SHORT).show()
                    vm.clearMessage()
                }
                state.downloaded?.let { file ->
                    openDownloaded(file)
                    vm.consumeDownload()
                }
                state.shareUrl?.let { url ->
                    val send = Intent(Intent.ACTION_SEND).apply {
                        type = "text/plain"
                        putExtra(Intent.EXTRA_TEXT, url)
                    }
                    startActivity(Intent.createChooser(send, "Paylaşım linki"))
                    // Also copy-friendly toast
                    Toast.makeText(this@MainActivity, url, Toast.LENGTH_LONG).show()
                    vm.consumeShareUrl()
                }
            }
        }
    }

    override fun onResume() {
        super.onResume()
        if (::backupStatus.isInitialized) refreshBackupStatus()
    }

    private fun showOfflineDownloads() {
        val files = vm.listOfflineDownloads()
        if (files.isEmpty()) {
            Toast.makeText(this, "Henüz çevrimdışı indirme yok", Toast.LENGTH_SHORT).show()
            return
        }
        val names = files.map { "${it.name} (${it.length() / 1024} KB)" }.toTypedArray()
        AlertDialog.Builder(this)
            .setTitle("İndirilenler (çevrimdışı)")
            .setItems(names) { _, which ->
                openDownloaded(files[which])
            }
            .setNegativeButton("Kapat", null)
            .show()
    }

    private fun openEntry(entry: FileEntry) {
        if (entry.kind == "folder") {
            vm.openFolder(entry)
            return
        }
        val mime = resolveMime(entry)
        if (isPreviewableMedia(mime)) {
            startActivity(
                Intent(this, MediaPreviewActivity::class.java).apply {
                    putExtra(MediaPreviewActivity.EXTRA_ID, entry.id)
                    putExtra(MediaPreviewActivity.EXTRA_NAME, entry.name)
                    putExtra(MediaPreviewActivity.EXTRA_MIME, mime)
                },
            )
        } else {
            vm.download(entry)
        }
    }

    private fun refreshBackupStatus() {
        val count = UploadedMediaDb(this).countUploaded()
        val net = if (session.wifiOnlyBackup) "yalnız Wi‑Fi" else "Wi‑Fi + mobil veri"
        val on = if (session.galleryBackupEnabled) "Açık" else "Kapalı"
        val last = session.lastBackupMessage.ifBlank { "Henüz çalışmadı" }
        backupStatus.text = "Durum: $on · $net · Yerelde işlenen: $count\n$last\nHedef klasör: TR Photos / yıl / ay"
    }

    private fun mediaPermissions(): Array<String> {
        return if (Build.VERSION.SDK_INT >= 33) {
            arrayOf(
                Manifest.permission.READ_MEDIA_IMAGES,
                Manifest.permission.READ_MEDIA_VIDEO,
                Manifest.permission.READ_MEDIA_AUDIO,
                Manifest.permission.POST_NOTIFICATIONS,
            )
        } else {
            arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
        }
    }

    private fun hasMediaPermission(): Boolean {
        val required = if (Build.VERSION.SDK_INT >= 33) {
            arrayOf(
                Manifest.permission.READ_MEDIA_IMAGES,
                Manifest.permission.READ_MEDIA_VIDEO,
                Manifest.permission.READ_MEDIA_AUDIO,
            )
        } else {
            arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
        }
        return required.all {
            ContextCompat.checkSelfPermission(this, it) == PackageManager.PERMISSION_GRANTED
        }
    }

    private fun requestMediaPermission() {
        permissionLauncher.launch(mediaPermissions())
    }

    private fun openDownloaded(file: File) {
        if (!file.exists() || file.length() == 0L) {
            Toast.makeText(this, "İndirilen dosya boş veya bulunamadı", Toast.LENGTH_LONG).show()
            return
        }
        val uri = try {
            FileProvider.getUriForFile(this, "$packageName.files", file)
        } catch (e: Exception) {
            Toast.makeText(this, "Dosya paylaşımı başarısız: ${e.message}", Toast.LENGTH_LONG).show()
            return
        }
        val mime = mimeFromFileName(file.name)
        val view = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mime)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        // Grant read permission to all apps that can open this type (fixes "doküman açılamadı").
        val matches = packageManager.queryIntentActivities(view, PackageManager.MATCH_DEFAULT_ONLY)
        for (info in matches) {
            grantUriPermission(
                info.activityInfo.packageName,
                uri,
                Intent.FLAG_GRANT_READ_URI_PERMISSION,
            )
        }
        try {
            val chooser = Intent.createChooser(view, "Dosyayı aç").apply {
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(chooser)
        } catch (_: ActivityNotFoundException) {
            Toast.makeText(
                this,
                "Bu dosya türünü ($mime) açacak uygulama yok. Dosya indirildi: ${file.name}",
                Toast.LENGTH_LONG,
            ).show()
        } catch (e: Exception) {
            Toast.makeText(this, "Açılamadı: ${e.message}", Toast.LENGTH_LONG).show()
        }
    }

    companion object {
        fun resolveMime(entry: FileEntry): String {
            val declared = entry.mimeType.trim()
            if (declared.isNotEmpty() && declared != "application/octet-stream") return declared
            return mimeFromFileName(entry.name).takeIf { it != "application/octet-stream" }
                ?: declared.ifBlank { "application/octet-stream" }
        }

        fun mimeFromFileName(name: String): String {
            val ext = name.substringAfterLast('.', missingDelimiterValue = "").lowercase()
            if (ext.isEmpty()) return "application/octet-stream"
            MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext)?.let { return it }
            return MediaPreviewActivity.guessMimeFromName(name)
        }

        fun isPreviewableMedia(mime: String): Boolean {
            return mime.startsWith("image/") ||
                mime.startsWith("video/") ||
                mime.startsWith("audio/")
        }
    }
}

private class FileAdapter(
    private val onOpen: (FileEntry) -> Unit,
    private val onLongPress: (FileEntry) -> Unit,
    private val onDownload: (FileEntry) -> Unit,
    private val onDelete: (FileEntry) -> Unit,
    private val onShare: (FileEntry) -> Unit,
    private val onStar: (FileEntry) -> Unit,
    private val onToggleCheck: (FileEntry) -> Unit,
) : RecyclerView.Adapter<FileAdapter.VH>() {
    private var items: List<FileEntry> = emptyList()
    private var selectionMode: Boolean = false
    private var selectedIds: Set<String> = emptySet()
    private var token: String = ""
    private var serverUrl: String = ""
    private var pathHint: String = ""

    fun submit(next: List<FileEntry>, selectionMode: Boolean = false, selectedIds: Set<String> = emptySet()) {
        items = next
        this.selectionMode = selectionMode
        this.selectedIds = selectedIds
        notifyDataSetChanged()
    }

    fun updateAuth(token: String, serverUrl: String) {
        this.token = token
        this.serverUrl = serverUrl.trimEnd('/')
    }

    fun updatePathHint(path: String) {
        pathHint = path
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
        val view = LayoutInflater.from(parent.context).inflate(R.layout.item_file, parent, false)
        return VH(view)
    }

    override fun getItemCount() = items.size

    override fun onBindViewHolder(holder: VH, position: Int) {
        val item = items[position]
        val mime = MainActivity.resolveMime(item)
        holder.name.text = item.name
        holder.meta.text = if (item.kind == "folder") "Klasör" else formatSize(item.sizeBytes)
        holder.itemView.setOnClickListener { onOpen(item) }
        holder.itemView.setOnLongClickListener {
            onLongPress(item)
            true
        }

        holder.check.visibility = if (selectionMode) View.VISIBLE else View.GONE
        holder.check.setOnCheckedChangeListener(null)
        holder.check.isChecked = item.id in selectedIds
        holder.check.setOnCheckedChangeListener { _, _ -> onToggleCheck(item) }

        holder.download.visibility = if (item.kind == "file" && !selectionMode) View.VISIBLE else View.GONE
        holder.delete.visibility = if (!selectionMode) View.VISIBLE else View.GONE
        holder.share.visibility = if (!selectionMode) View.VISIBLE else View.GONE
        holder.star.visibility = if (!selectionMode) View.VISIBLE else View.GONE
        holder.star.text = if (item.starred) "★" else "☆"
        holder.download.setOnClickListener { onDownload(item) }
        holder.delete.setOnClickListener { onDelete(item) }
        holder.share.setOnClickListener { onShare(item) }
        holder.star.setOnClickListener { onStar(item) }

        holder.thumb.setImageDrawable(null)
        holder.thumb.setBackgroundColor(Color.parseColor("#E8F2FC"))
        when {
            item.kind == "folder" -> {
                holder.thumb.setBackgroundColor(Color.parseColor("#0B5CAD"))
            }
            mime.startsWith("image/") && token.isNotBlank() && serverUrl.isNotBlank() -> {
                val url = "$serverUrl/api/files/download/${item.id}?inline=1"
                holder.thumb.load(url) {
                    headers(Headers.headersOf("Authorization", "Bearer $token"))
                    size(96, 96)
                }
            }
        }

        val badge = when {
            item.starred -> "Yıldızlı"
            MainActivity.isPreviewableMedia(mime) -> "Medya"
            pathHint.contains("TR Photos", ignoreCase = true) -> "Yedek"
            else -> null
        }
        if (badge != null) {
            holder.badge.visibility = View.VISIBLE
            holder.badge.text = badge
        } else {
            holder.badge.visibility = View.GONE
            holder.badge.text = ""
        }
    }

    private fun formatSize(bytes: Long): String {
        if (bytes < 1024) return "$bytes B"
        val kb = bytes / 1024.0
        if (kb < 1024) return String.format("%.1f KB", kb)
        val mb = kb / 1024.0
        if (mb < 1024) return String.format("%.1f MB", mb)
        return String.format("%.1f GB", mb / 1024.0)
    }

    class VH(view: View) : RecyclerView.ViewHolder(view) {
        val check: android.widget.CheckBox = view.findViewById(R.id.itemCheck)
        val thumb: ImageView = view.findViewById(R.id.itemThumb)
        val name: TextView = view.findViewById(R.id.itemName)
        val meta: TextView = view.findViewById(R.id.itemMeta)
        val badge: TextView = view.findViewById(R.id.itemBadge)
        val star: Button = view.findViewById(R.id.itemStar)
        val share: Button = view.findViewById(R.id.itemShare)
        val download: Button = view.findViewById(R.id.itemDownload)
        val delete: Button = view.findViewById(R.id.itemDelete)
    }
}
