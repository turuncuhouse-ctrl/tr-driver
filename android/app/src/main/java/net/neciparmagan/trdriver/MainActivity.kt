package net.neciparmagan.trdriver

import android.Manifest
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
        onOpen = { entry -> openEntry(entry) },
        onDownload = { vm.download(it) },
        onDelete = { entry ->
            AlertDialog.Builder(this)
                .setMessage("\"${entry.name}\" silinsin mi?")
                .setPositiveButton("Sil") { _, _ -> vm.deleteEntry(entry) }
                .setNegativeButton("İptal", null)
                .show()
        },
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
                    adapter.submit(state.files)
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
            }
        }
    }

    override fun onResume() {
        super.onResume()
        if (::backupStatus.isInitialized) refreshBackupStatus()
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
                Manifest.permission.POST_NOTIFICATIONS,
            )
        } else {
            arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
        }
    }

    private fun hasMediaPermission(): Boolean {
        val required = if (Build.VERSION.SDK_INT >= 33) {
            arrayOf(Manifest.permission.READ_MEDIA_IMAGES, Manifest.permission.READ_MEDIA_VIDEO)
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
        val uri = FileProvider.getUriForFile(this, "$packageName.files", file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, contentResolver.getType(uri) ?: "*/*")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        runCatching { startActivity(Intent.createChooser(intent, "Dosyayı aç")) }
    }

    companion object {
        fun resolveMime(entry: FileEntry): String {
            val declared = entry.mimeType.trim()
            if (declared.isNotEmpty() && declared != "application/octet-stream") return declared
            val ext = entry.name.substringAfterLast('.', missingDelimiterValue = "").lowercase()
            if (ext.isEmpty()) return declared
            return MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: declared
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
    private val onDownload: (FileEntry) -> Unit,
    private val onDelete: (FileEntry) -> Unit,
) : RecyclerView.Adapter<FileAdapter.VH>() {
    private var items: List<FileEntry> = emptyList()
    private var token: String = ""
    private var serverUrl: String = ""
    private var pathHint: String = ""

    fun submit(next: List<FileEntry>) {
        items = next
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
        holder.meta.text = if (item.kind == "folder") "Klasör" else "${item.sizeBytes} bayt"
        holder.itemView.setOnClickListener { onOpen(item) }
        holder.download.visibility = if (item.kind == "file") View.VISIBLE else View.GONE
        holder.download.setOnClickListener { onDownload(item) }
        holder.delete.setOnClickListener { onDelete(item) }

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

    class VH(view: View) : RecyclerView.ViewHolder(view) {
        val thumb: ImageView = view.findViewById(R.id.itemThumb)
        val name: TextView = view.findViewById(R.id.itemName)
        val meta: TextView = view.findViewById(R.id.itemMeta)
        val badge: TextView = view.findViewById(R.id.itemBadge)
        val download: Button = view.findViewById(R.id.itemDownload)
        val delete: Button = view.findViewById(R.id.itemDelete)
    }
}
