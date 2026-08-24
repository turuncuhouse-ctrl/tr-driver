package net.neciparmagan.trdriver

import android.content.ActivityNotFoundException
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.MimeTypeMap
import android.widget.Button
import android.widget.EditText
import android.widget.ImageButton
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.PopupMenu
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
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout
import coil.load
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.SessionStore
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
    private lateinit var avatarBadge: TextView
    private lateinit var backupChip: TextView
    private lateinit var backupInline: LinearLayout
    private lateinit var backupInlineBar: ProgressBar
    private lateinit var backupInlineText: TextView
    private lateinit var swipeRefresh: SwipeRefreshLayout
    private lateinit var miniPlayer: View
    private lateinit var miniPlayerTitle: TextView
    private lateinit var inputDisplayName: EditText
    private lateinit var btnRegister: Button
    private lateinit var inputServer: EditText
    private lateinit var inputEmail: EditText
    private lateinit var inputPassword: EditText

    private var registerNameVisible = false
    private val backupPollHandler = Handler(Looper.getMainLooper())
    private val backupPollRunnable = object : Runnable {
        override fun run() {
            refreshBackupUi()
            backupPollHandler.postDelayed(this, 2_000L)
        }
    }
    private val backupPrefsListener =
        SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
            when (key) {
                SessionStore.KEY_BACKUP_ACTIVE,
                SessionStore.KEY_BACKUP_CURRENT,
                SessionStore.KEY_BACKUP_DONE,
                SessionStore.KEY_BACKUP_PENDING,
                SessionStore.KEY_BACKUP_PERCENT,
                SessionStore.KEY_BACKUP_BYTES_SENT,
                SessionStore.KEY_BACKUP_BYTES_TOTAL,
                SessionStore.KEY_LAST_MSG,
                SessionStore.KEY_GALLERY_ON,
                SessionStore.KEY_LAST_MSG,
                SessionStore.KEY_GALLERY_ON,
                -> runOnUiThread { refreshBackupUi() }
            }
        }

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
            val email = json.optString("email").takeIf { it.isNotBlank() }
            val password = json.optString("password").takeIf { it.isNotBlank() }

            if (token.isNotBlank()) {
                vm.redeemQr(token, server)
                return@registerForActivityResult
            }
            // Connection QR: fill login form (password optional)
            if (server != null || email != null || password != null) {
                if (server != null) {
                    inputServer.setText(server)
                    vm.updateServer(server)
                }
                if (email != null) {
                    inputEmail.setText(email)
                    vm.updateEmail(email)
                }
                if (password != null) {
                    inputPassword.setText(password)
                    vm.updatePassword(password)
                }
                Toast.makeText(this, "QR alanları dolduruldu", Toast.LENGTH_SHORT).show()
                return@registerForActivityResult
            }
            Toast.makeText(this, "QR kodunda challengeToken veya giriş bilgisi yok", Toast.LENGTH_LONG).show()
        }.onFailure {
            Toast.makeText(this, "QR okunamadı: ${it.message}", Toast.LENGTH_LONG).show()
        }
    }

    private val musicReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            refreshMiniPlayer()
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
        avatarBadge = findViewById(R.id.avatarBadge)
        backupChip = findViewById(R.id.backupChip)
        backupInline = findViewById(R.id.backupInline)
        backupInlineBar = findViewById(R.id.backupInlineBar)
        backupInlineText = findViewById(R.id.backupInlineText)
        swipeRefresh = findViewById(R.id.swipeRefresh)
        miniPlayer = findViewById(R.id.miniPlayer)
        miniPlayerTitle = findViewById(R.id.miniPlayerTitle)
        inputDisplayName = findViewById(R.id.inputDisplayName)
        btnRegister = findViewById(R.id.btnRegister)
        inputServer = findViewById(R.id.inputServer)
        inputEmail = findViewById(R.id.inputEmail)
        inputPassword = findViewById(R.id.inputPassword)

        list.layoutManager = LinearLayoutManager(this)
        list.adapter = adapter

        val otp = findViewById<EditText>(R.id.inputOtp)
        val otpLabel = findViewById<TextView>(R.id.otpLabel)

        findViewById<Button>(R.id.btnLogin).setOnClickListener {
            vm.updateServer(inputServer.text.toString())
            vm.updateEmail(inputEmail.text.toString())
            vm.updatePassword(inputPassword.text.toString())
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
            val mail = inputEmail.text.toString().trim()
            val pass = inputPassword.text.toString()
            if (name.isEmpty() || mail.isEmpty() || pass.isEmpty()) {
                Toast.makeText(this, "Ad, e-posta ve şifre gerekli", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            vm.updateServer(inputServer.text.toString())
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

        findViewById<View>(R.id.headerProfile).setOnClickListener { showAccountSheet() }
        findViewById<Button>(R.id.btnActions).setOnClickListener { showActionsMenu(it) }
        findViewById<Button>(R.id.btnSearch).setOnClickListener {
            vm.search(findViewById<EditText>(R.id.inputSearch).text.toString())
        }
        findViewById<EditText>(R.id.inputSearch).setOnEditorActionListener { _, _, _ ->
            vm.search(findViewById<EditText>(R.id.inputSearch).text.toString())
            true
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
        crumbs.setOnClickListener {
            val state = vm.state.value
            if (state.crumbs.size > 1) vm.goToCrumb(state.crumbs.lastIndex - 1)
        }
        backupChip.setOnClickListener {
            startActivity(Intent(this, BackupSettingsActivity::class.java))
        }
        swipeRefresh.setOnRefreshListener { vm.loadFiles() }

        findViewById<Button>(R.id.btnMiniToggle).setOnClickListener {
            startService(Intent(this, MusicService::class.java).setAction(MusicService.ACTION_TOGGLE))
        }
        findViewById<Button>(R.id.btnMiniOpen).setOnClickListener {
            startActivity(
                Intent(this, PlayerActivity::class.java).apply {
                    putExtra(MusicService.EXTRA_TITLE, MusicService.currentTitle)
                    putExtra(MusicService.EXTRA_URL, MusicService.currentUrl)
                    putExtra(MusicService.EXTRA_TOKEN, MusicService.currentToken)
                },
            )
        }
        miniPlayer.setOnClickListener {
            findViewById<Button>(R.id.btnMiniOpen).performClick()
        }

        lifecycleScope.launch {
            vm.state.collectLatest { state ->
                progress.visibility = if (state.busy && state.transferLabel == null) View.VISIBLE else View.GONE
                if (!state.busy) swipeRefresh.isRefreshing = false
                if (!state.bootstrapped) return@collectLatest

                adapter.updateAuth(session.token.orEmpty(), session.serverUrl)
                adapter.updatePathHint(state.crumbs.joinToString("/") { it.name })

                if (!state.loggedIn) {
                    loginPanel.visibility = View.VISIBLE
                    filesPanel.visibility = View.GONE
                    inputServer.setText(state.serverUrl)
                    inputEmail.setText(state.email)
                    otpLabel.visibility = if (state.requires2FA) View.VISIBLE else View.GONE
                    otp.visibility = if (state.requires2FA) View.VISIBLE else View.GONE
                } else {
                    loginPanel.visibility = View.GONE
                    filesPanel.visibility = View.VISIBLE
                    val mail = state.user?.email ?: state.email
                    titleEmail.text = mail
                    avatarBadge.text = mail.firstOrNull()?.uppercaseChar()?.toString() ?: "?"
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
                    if (state.transferLabel != null) {
                        backupInline.visibility = View.VISIBLE
                        backupInlineBar.progress = state.transferPercent
                        backupInlineText.text = state.transferLabel
                    } else {
                        refreshBackupUi()
                    }
                    refreshMiniPlayer()
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
                    Toast.makeText(this@MainActivity, url, Toast.LENGTH_LONG).show()
                    vm.consumeShareUrl()
                }
            }
        }
    }

    override fun onStart() {
        super.onStart()
        val filter = IntentFilter(MusicService.ACTION_STATE)
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(musicReceiver, filter, RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            registerReceiver(musicReceiver, filter)
        }
        refreshMiniPlayer()
        session.registerBackupListener(backupPrefsListener)
        backupPollHandler.post(backupPollRunnable)
    }

    override fun onStop() {
        backupPollHandler.removeCallbacks(backupPollRunnable)
        session.unregisterBackupListener(backupPrefsListener)
        runCatching { unregisterReceiver(musicReceiver) }
        super.onStop()
    }

    override fun onResume() {
        super.onResume()
        if (::backupChip.isInitialized) refreshBackupUi()
        refreshMiniPlayer()
        if (session.isLoggedIn) {
            AppUpdateHelper.check(this, force = false, silentIfCurrent = true)
        }
    }

    private fun refreshBackupUi() {
        if (!::backupChip.isInitialized) return
        val line = session.backupStatusLine()
        backupChip.text = if (line.length > 42) line.take(39) + "…" else line
        val showBar = session.isLoggedIn && session.galleryBackupEnabled &&
            (session.backupActive || session.backupPendingCount > 0)
        if (!showBar) {
            backupInline.visibility = View.GONE
            return
        }
        backupInline.visibility = View.VISIBLE
        backupInlineBar.progress = session.backupDisplayPercent
        val bytes = session.backupFileBytesLabel()
        backupInlineText.text = when {
            session.backupActive && session.backupCurrentFile.isNotBlank() ->
                buildString {
                    append("Yedekleniyor · ${session.backupCurrentFile}")
                    if (bytes.isNotBlank()) append(" · $bytes")
                    append(" · %${session.backupDisplayPercent}")
                    if (session.backupPendingCount > 0) append(" · kalan ${session.backupPendingCount}")
                }
            session.backupPendingCount > 0 ->
                "Yedek bekliyor · kalan ${session.backupPendingCount} · %${session.backupPercent}"
            else -> session.lastBackupMessage.ifBlank { "Yedek hazır" }
        }
    }

    private fun showAccountSheet() {
        val mail = titleEmail.text?.toString().orEmpty().ifBlank { session.email }
        AlertDialog.Builder(this)
            .setTitle("Hesap")
            .setMessage("E-posta: $mail\nSunucu: ${session.serverUrl}")
            .setPositiveButton("Çıkış") { _, _ ->
                session.galleryBackupEnabled = false
                GalleryBackupWorker.schedule(this)
                registerNameVisible = false
                inputDisplayName.visibility = View.GONE
                btnRegister.text = "Üye ol"
                vm.logout()
            }
            .setNegativeButton("Kapat", null)
            .show()
    }

    private fun showActionsMenu(anchor: View) {
        PopupMenu(this, anchor).apply {
            menu.add(0, 1, 0, "★ Yıldızlı")
            menu.add(0, 2, 1, "Son")
            menu.add(0, 3, 2, "İndirilenler")
            menu.add(0, 4, 3, "Seç")
            menu.add(0, 5, 4, "Yükle")
            menu.add(0, 6, 5, "Yeni klasör")
            menu.add(0, 10, 6, "Araç kabul")
            menu.add(0, 7, 7, "Yedekleme ayarları")
            menu.add(0, 8, 8, "Müzik")
            menu.add(0, 9, 9, "Güncellemeyi kontrol et")
            setOnMenuItemClickListener { item ->
                when (item.itemId) {
                    1 -> vm.showStarred()
                    2 -> vm.showRecent()
                    3 -> showOfflineDownloads()
                    4 -> {
                        val first = vm.state.value.files.firstOrNull()
                        if (first != null) vm.enterSelection(first.id)
                        else Toast.makeText(this@MainActivity, "Önce dosya listesi gerekli", Toast.LENGTH_SHORT).show()
                    }
                    5 -> picker.launch("*/*")
                    6 -> promptNewFolder()
                    10 -> startActivity(Intent(this@MainActivity, VehicleIntakeActivity::class.java))
                    7 -> startActivity(Intent(this@MainActivity, BackupSettingsActivity::class.java))
                    8 -> {
                        if (MusicService.isSessionActive) {
                            startActivity(Intent(this@MainActivity, PlayerActivity::class.java))
                        } else {
                            Toast.makeText(
                                this@MainActivity,
                                "Bir ses dosyasına dokunarak müzik açın",
                                Toast.LENGTH_SHORT,
                            ).show()
                        }
                    }
                    9 -> AppUpdateHelper.check(this@MainActivity, force = true, silentIfCurrent = false)
                }
                true
            }
            show()
        }
    }

    private fun promptNewFolder() {
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

    private fun refreshMiniPlayer() {
        if (!::miniPlayer.isInitialized) return
        if (MusicService.isSessionActive && MusicService.currentTitle.isNotBlank()) {
            miniPlayer.visibility = View.VISIBLE
            miniPlayerTitle.text = MusicService.currentTitle
            findViewById<Button>(R.id.btnMiniToggle).text =
                if (MusicService.isPlaying) "⏸" else "▶"
        } else {
            miniPlayer.visibility = View.GONE
        }
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
        if (mime.startsWith("audio/")) {
            val token = session.token.orEmpty()
            val url = "${session.serverUrl.trimEnd('/')}/api/files/download/${entry.id}?inline=1"
            PlayerActivity.start(this, entry.name, url, token)
            return
        }
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
        if (mime.startsWith("audio/")) {
            PlayerActivity.start(this, file.name, uri.toString(), "")
            return
        }
        val view = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mime)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
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
        val starMark = if (item.starred) "★ " else ""
        holder.name.text = "$starMark${item.name}"
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

        holder.menu.visibility = if (selectionMode) View.GONE else View.VISIBLE
        holder.menu.setOnClickListener { anchor ->
            PopupMenu(anchor.context, anchor).apply {
                if (item.kind == "file") menu.add(0, 1, 0, "İndir")
                menu.add(0, 2, 1, "Paylaş")
                menu.add(0, 3, 2, if (item.starred) "Yıldızı kaldır" else "Yıldızla")
                menu.add(0, 4, 3, "Sil")
                setOnMenuItemClickListener { mi ->
                    when (mi.itemId) {
                        1 -> onDownload(item)
                        2 -> onShare(item)
                        3 -> onStar(item)
                        4 -> onDelete(item)
                    }
                    true
                }
                show()
            }
        }

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
            pathHint.contains("TR Backup", ignoreCase = true) -> "Yedek"
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
        val menu: ImageButton = view.findViewById(R.id.itemMenu)
    }
}
