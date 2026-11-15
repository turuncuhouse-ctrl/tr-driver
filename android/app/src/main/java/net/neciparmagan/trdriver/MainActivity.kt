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
import androidx.recyclerview.widget.GridLayoutManager
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout
import coil.load
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.backup.GalleryBackupWorker
import net.neciparmagan.trdriver.calls.CallsActivity
import net.neciparmagan.trdriver.calls.LocalTelListenerService
import net.neciparmagan.trdriver.calls.LocalTelConfigApi
import net.neciparmagan.trdriver.calls.LocalTelProviderType
import net.neciparmagan.trdriver.calls.CallController
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.MimeHelper
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.StorageMeter
import net.neciparmagan.trdriver.presentation.DriveViewModel
import okhttp3.Headers
import org.json.JSONObject
import java.io.File

class MainActivity : AppCompatActivity() {
    private val vm: DriveViewModel by viewModels()
    private lateinit var session: SessionStore
    private var lastLocalTelConfigFetchAt = 0L

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
    private lateinit var permissionWarningCard: View
    private lateinit var permissionWarningText: TextView
    private lateinit var swipeRefresh: SwipeRefreshLayout
    private var permissionWizardLaunched = false
    private lateinit var miniPlayer: View
    private lateinit var miniPlayerTitle: TextView
    private lateinit var inputDisplayName: EditText
    private lateinit var inputPhone: EditText
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
                SessionStore.KEY_BACKUP_SUCCESS,
                SessionStore.KEY_LAST_MSG,
                SessionStore.KEY_GALLERY_ON,
                -> runOnUiThread { refreshBackupUi() }
            }
        }

    private val adapter = FileAdapter(
        onOpen = { entry ->
            if (vm.state.value.selectionMode) {
                vm.toggleSelection(entry.id)
            } else if (driveSection == DriveSection.TRASH) {
                showTrashContextMenu(entry, list)
            } else {
                openEntry(entry)
            }
        },
        onLongPress = { entry ->
            if (driveSection == DriveSection.TRASH) showTrashContextMenu(entry, list)
            else showFileContextMenu(entry, list)
        },
        onDownload = { vm.download(it) },
        onDelete = { entry ->
            if (driveSection == DriveSection.TRASH) {
                confirmPurge(entry)
            } else {
                AlertDialog.Builder(this)
                    .setMessage("\"${entry.name}\" çöp kutusuna taşınsın mı?")
                    .setPositiveButton("Çöp kutusuna taşı") { _, _ -> vm.deleteEntry(entry) }
                    .setNegativeButton("İptal", null)
                    .show()
            }
        },
        onRestore = { entry -> vm.restoreEntry(entry) },
        onPurge = { entry -> confirmPurge(entry) },
        onShare = { vm.shareEntry(it) },
        onStar = { vm.toggleStar(it) },
        onRename = { entry -> promptRename(entry) },
        onMove = { entry -> promptMove(entry) },
        onToggleCheck = { vm.toggleSelection(it.id) },
        onAddIntakePhotos = { entry -> openIntakeForPlate(entry.name, entry.id) },
    )
    private var gridLayout = false
    private var driveSection: DriveSection = DriveSection.FILES

    private enum class DriveSection { HOME, FILES, STARRED, TRASH }

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
        permissionWarningCard = findViewById(R.id.permissionWarningCard)
        permissionWarningText = findViewById(R.id.permissionWarningText)
        findViewById<Button>(R.id.permissionWarningButton).setOnClickListener { openPermissionWizard() }
        swipeRefresh = findViewById(R.id.swipeRefresh)
        miniPlayer = findViewById(R.id.miniPlayer)
        miniPlayerTitle = findViewById(R.id.miniPlayerTitle)
        inputDisplayName = findViewById(R.id.inputDisplayName)
        inputPhone = findViewById(R.id.inputPhone)
        btnRegister = findViewById(R.id.btnRegister)
        inputServer = findViewById(R.id.inputServer)
        inputEmail = findViewById(R.id.inputEmail)
        inputPassword = findViewById(R.id.inputPassword)

        list.layoutManager = LinearLayoutManager(this)
        list.adapter = adapter
        gridLayout = session.filesGridLayout
        applyFilesLayout()

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
                inputPhone.visibility = View.VISIBLE
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
            vm.register(inputPhone.text?.toString().orEmpty())
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
        findViewById<Button>(R.id.btnLoginCalls).setOnClickListener {
            startActivity(Intent(this, CallsActivity::class.java))
        }

        findViewById<View>(R.id.avatarBadge).setOnClickListener { showAccountSheet() }
        findViewById<Button>(R.id.btnActions).setOnClickListener { showActionsMenu(it) }
        findViewById<Button>(R.id.btnPhotos).setOnClickListener {
            startActivity(Intent(this, PhotosLibraryActivity::class.java))
        }
        findViewById<Button>(R.id.btnCalls).setOnClickListener {
            startActivity(Intent(this, CallsActivity::class.java))
        }
        findViewById<Button>(R.id.btnLayout).setOnClickListener {
            gridLayout = !gridLayout
            session.filesGridLayout = gridLayout
            applyFilesLayout()
        }
        findViewById<Button>(R.id.btnSearch).setOnClickListener {
            runDriveSearch()
        }
        findViewById<EditText>(R.id.inputSearch).setOnEditorActionListener { _, actionId, _ ->
            if (actionId == android.view.inputmethod.EditorInfo.IME_ACTION_SEARCH ||
                actionId == android.view.inputmethod.EditorInfo.IME_ACTION_DONE
            ) {
                runDriveSearch()
                true
            } else {
                false
            }
        }
        findViewById<Button>(R.id.btnCancelSelect).setOnClickListener { vm.clearSelection() }
        findViewById<Button>(R.id.btnDeleteSelected).setOnClickListener {
            if (driveSection == DriveSection.TRASH) {
                AlertDialog.Builder(this)
                    .setTitle("Kalıcı olarak sil")
                    .setMessage("Seçili öğeler kalıcı olarak silinsin mi? Bu işlem geri alınamaz.")
                    .setPositiveButton("Kalıcı sil") { _, _ -> vm.purgeSelected() }
                    .setNegativeButton("İptal", null)
                    .show()
            } else {
                AlertDialog.Builder(this)
                    .setMessage("Seçili öğeler çöp kutusuna taşınsın mı?")
                    .setPositiveButton("Çöp kutusuna taşı") { _, _ -> vm.deleteSelected() }
                    .setNegativeButton("İptal", null)
                    .show()
            }
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
        findViewById<View>(R.id.fabDrive).setOnClickListener { showFabMenu(it) }
        findViewById<Button>(R.id.navHome).setOnClickListener { selectDriveSection(DriveSection.HOME) }
        findViewById<Button>(R.id.navFiles).setOnClickListener { selectDriveSection(DriveSection.FILES) }
        findViewById<Button>(R.id.navStarred).setOnClickListener { selectDriveSection(DriveSection.STARRED) }
        findViewById<Button>(R.id.navMore).setOnClickListener { showActionsMenu(it) }
        styleDriveNav()
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
                    ensureLocalTelConfig()
                    val mail = state.user?.email ?: state.email
                    titleEmail.text = mail
                    avatarBadge.text = mail.firstOrNull()?.uppercaseChar()?.toString() ?: "?"
                    crumbs.text = state.crumbs.joinToString(" / ") { it.name }
                    state.crumbs.lastOrNull()?.id?.takeIf { it.isNotBlank() }?.let {
                        session.lastBrowseFolderId = it
                    }
                    adapter.setTrashMode(state.trashMode)
                    adapter.submit(state.files, state.selectionMode, state.selectedIds)
                    findViewById<View>(R.id.driveEmpty).visibility =
                        if (state.files.isEmpty() && !state.busy) View.VISIBLE else View.GONE
                    findViewById<TextView>(R.id.driveEmpty).text = when {
                        driveSection == DriveSection.STARRED -> "Yıldızlı öğe yok\nDosyalarda ★ ile işaretleyin"
                        driveSection == DriveSection.HOME -> "Son açılan yok\nDosyalardan bir öğe açın"
                        driveSection == DriveSection.TRASH -> "Çöp kutusu boş\nSilinen öğeler burada görünür"
                        else -> "Bu klasör boş\n+ ile dosya yükleyin veya klasör oluşturun"
                    }
                    findViewById<View>(R.id.selectionBar).visibility =
                        if (state.selectionMode) View.VISIBLE else View.GONE
                    // Trashed items can only be purged in bulk; download/share need a live file.
                    findViewById<View>(R.id.btnDownloadSelected).visibility =
                        if (state.trashMode) View.GONE else View.VISIBLE
                    findViewById<View>(R.id.btnShareSelected).visibility =
                        if (state.trashMode) View.GONE else View.VISIBLE
                    findViewById<Button>(R.id.btnDeleteSelected).text =
                        if (state.trashMode) "Kalıcı sil" else "Sil"
                    findViewById<TextView>(R.id.selectionCount).text = "${state.selectedIds.size} seçili"
                    findViewById<View>(R.id.offlineBanner).visibility =
                        if (state.offline) View.VISIBLE else View.GONE
                    findViewById<TextView>(R.id.driveSectionTitle).text = when (driveSection) {
                        DriveSection.HOME -> "Ana sayfa"
                        DriveSection.STARRED -> "Yıldızlı"
                        DriveSection.TRASH -> "Çöp kutusu"
                        DriveSection.FILES -> "TR Driver"
                    }
                    styleDriveNav()
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
                state.pdfPreview?.let { file ->
                    startActivity(PdfPreviewActivity.intent(this@MainActivity, file.absolutePath))
                    vm.consumePdfPreview()
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
        AppUpdateHelper.onMainResume(this)
        val cameFromUpdateNotification =
            intent?.getBooleanExtra("tr_install_update", false) == true
        if (cameFromUpdateNotification) {
            intent.removeExtra("tr_install_update")
        }
        // Version check runs at every launch (public endpoint, no session
        // needed); a background update notification opens the dialog at once.
        AppUpdateHelper.check(
            this,
            force = cameFromUpdateNotification,
            silentIfCurrent = !cameFromUpdateNotification,
        )
        runAppBootstrapIfNeeded()
        refreshPermissionWarning()
        if (session.isLoggedIn) {
            ensureLocalTelConfig()
        }
        if (session.localTelEnabled && session.localTelAutoRegister) {
            LocalTelListenerService.syncState(this)
        }
    }

    private fun ensureLocalTelConfig() {
        val existing = session.loadLocalTelConfig()
        if (existing.providerType == LocalTelProviderType.SIP && existing.enabled) return
        val now = System.currentTimeMillis()
        if (now - lastLocalTelConfigFetchAt < 2 * 60_000L) return
        lastLocalTelConfigFetchAt = now
        lifecycleScope.launch {
            LocalTelConfigApi(session).fetchGatewayConfig().onSuccess { config ->
                session.saveLocalTelConfig(config)
                // Do not tear down a healthy socket every time the app opens.
                val registered = CallController.providerStatus.value.state ==
                    net.neciparmagan.trdriver.calls.ProviderRegistrationState.REGISTERED
                CallController.ensureProviderStarted(this@MainActivity, forceRestart = !registered)
                LocalTelListenerService.syncState(this@MainActivity)
            }
        }
    }

    private fun runAppBootstrapIfNeeded() {
        if (AppBootstrap.isBootstrapped(this)) {
            if (session.anyBackupEnabled() && session.isLoggedIn) {
                net.neciparmagan.trdriver.backup.TrKeepAliveService.startIfNeeded(this)
            }
            return
        }
        if (!permissionWizardLaunched) {
            permissionWizardLaunched = true
            openPermissionWizard()
        }
    }

    private fun openPermissionWizard() {
        startActivity(Intent(this, PermissionWizardActivity::class.java))
    }

    private fun refreshPermissionWarning() {
        if (!::permissionWarningCard.isInitialized) return
        val missing = PermissionWizardActivity.missingImportantPermissions(this)
        permissionWarningCard.visibility = if (missing.isEmpty()) View.GONE else View.VISIBLE
        if (missing.isNotEmpty()) {
            permissionWarningText.text = "Eksik izin: ${missing.take(3).joinToString(", ")}${if (missing.size > 3) " ve ${missing.size - 3} izin" else ""}. İlgili özellikler duraklatıldı."
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

    private fun runDriveSearch() {
        val input = findViewById<EditText>(R.id.inputSearch)
        val q = input.text?.toString().orEmpty().trim()
        val imm = getSystemService(Context.INPUT_METHOD_SERVICE) as? android.view.inputmethod.InputMethodManager
        imm?.hideSoftInputFromWindow(input.windowToken, 0)
        vm.search(q)
    }

    private fun selectDriveSection(section: DriveSection) {
        driveSection = section
        styleDriveNav()
        when (section) {
            DriveSection.HOME -> vm.showRecent()
            DriveSection.FILES -> vm.goToFilesRoot()
            DriveSection.STARRED -> vm.showStarred()
            DriveSection.TRASH -> vm.showTrash()
        }
    }

    private fun confirmPurge(entry: FileEntry) {
        AlertDialog.Builder(this)
            .setTitle("Kalıcı olarak sil")
            .setMessage("\"${entry.name}\" kalıcı olarak silinsin mi? Bu işlem geri alınamaz.")
            .setPositiveButton("Kalıcı sil") { _, _ -> vm.purgeEntry(entry) }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun showTrashContextMenu(entry: FileEntry, anchor: View) {
        PopupMenu(this, anchor).apply {
            menu.add(0, 9, 0, "Seç")
            menu.add(0, 1, 1, "Geri yükle")
            menu.add(0, 2, 2, "Kalıcı sil")
            setOnMenuItemClickListener { mi ->
                when (mi.itemId) {
                    9 -> vm.enterSelection(entry.id)
                    1 -> vm.restoreEntry(entry)
                    2 -> confirmPurge(entry)
                }
                true
            }
            show()
        }
    }

    private fun styleDriveNav() {
        fun style(btn: Button, active: Boolean) {
            btn.setTextColor(
                ContextCompat.getColor(this, if (active) R.color.tr_blue else R.color.tr_ink),
            )
            btn.typeface = if (active) {
                android.graphics.Typeface.DEFAULT_BOLD
            } else {
                android.graphics.Typeface.DEFAULT
            }
        }
        style(findViewById(R.id.navHome), driveSection == DriveSection.HOME)
        style(findViewById(R.id.navFiles), driveSection == DriveSection.FILES)
        style(findViewById(R.id.navStarred), driveSection == DriveSection.STARRED)
        style(findViewById(R.id.navMore), false)
    }

    private fun showFabMenu(anchor: View) {
        PopupMenu(this, anchor).apply {
            menu.add(0, 1, 0, "Dosya yükle")
            menu.add(0, 2, 1, "Yeni klasör")
            currentIntakePlate()?.let { plate ->
                menu.add(0, 3, 2, "Fotoğraf ekle · $plate")
            }
            setOnMenuItemClickListener { item ->
                when (item.itemId) {
                    1 -> picker.launch("*/*")
                    2 -> promptNewFolder()
                    3 -> currentIntakePlate()?.let { openIntakeForPlate(it, vm.state.value.crumbs.last().id) }
                }
                true
            }
            show()
        }
    }

    private fun showFileContextMenu(entry: FileEntry, anchor: View) {
        PopupMenu(this, anchor).apply {
            menu.add(0, 9, 0, "Seç")
            if (entry.kind == "file") menu.add(0, 1, 1, "İndir")
            menu.add(0, 2, 2, "Paylaş")
            menu.add(0, 3, 3, if (entry.starred) "Yıldızı kaldır" else "Yıldızla")
            menu.add(0, 5, 4, "Yeniden adlandır")
            menu.add(0, 6, 5, "Taşı")
            menu.add(0, 4, 6, "Sil")
            setOnMenuItemClickListener { mi ->
                when (mi.itemId) {
                    9 -> vm.enterSelection(entry.id)
                    1 -> vm.download(entry)
                    2 -> vm.shareEntry(entry)
                    3 -> vm.toggleStar(entry)
                    5 -> promptRename(entry)
                    6 -> promptMove(entry)
                    4 -> {
                        AlertDialog.Builder(this@MainActivity)
                            .setMessage("\"${entry.name}\" çöp kutusuna taşınsın mı?")
                            .setPositiveButton("Çöp kutusuna taşı") { _, _ -> vm.deleteEntry(entry) }
                            .setNegativeButton("İptal", null)
                            .show()
                    }
                }
                true
            }
            show()
        }
    }

    private fun showAccountSheet() {
        val mail = titleEmail.text?.toString().orEmpty().ifBlank { session.email }
        val user = vm.state.value.user
        val storage = if (user != null) {
            StorageMeter.summary(user.usedBytes, user.quotaBytes, SessionStore::formatBytes)
        } else {
            "Depolama: yükleniyor…"
        }
        AlertDialog.Builder(this)
            .setTitle("Hesap")
            .setMessage("E-posta: $mail\nSunucu: ${session.serverUrl}\n$storage")
            .setPositiveButton("Çıkış") { _, _ ->
                session.galleryBackupEnabled = false
                GalleryBackupWorker.schedule(this)
                if (session.localTelSyncToServer) {
                    LocalTelListenerService.stop(this)
                }
                registerNameVisible = false
                inputDisplayName.visibility = View.GONE
                inputPhone.visibility = View.GONE
                inputPhone.setText("")
                btnRegister.text = "Üye ol"
                vm.logout()
            }
            .setNeutralButton("TR Galeri") { _, _ ->
                startActivity(Intent(this, PhotosLibraryActivity::class.java))
            }
            .setNegativeButton("Tamam", null)
            .show()
    }

    private fun showActionsMenu(anchor: View) {
        PopupMenu(this, anchor).apply {
            menu.add(0, 1, 0, "★ Yıldızlı")
            menu.add(0, 2, 1, "Son")
            menu.add(0, 14, 2, "Çöp kutusu")
            menu.add(0, 3, 3, "İndirilenler")
            menu.add(0, 4, 4, "Seç")
            menu.add(0, 5, 5, "Yükle")
            menu.add(0, 6, 6, "Yeni klasör")
            currentIntakePlate()?.let { plate ->
                menu.add(0, 11, 7, "Fotoğraf ekle · $plate")
            }
            menu.add(0, 10, 8, "Araç kabul")
            menu.add(0, 12, 9, "TR Photos")
            menu.add(0, 15, 10, "LocalTel Aramalar")
            menu.add(0, 7, 11, "Yedekleme ayarları")
            menu.add(0, 16, 12, "İzinler")
            menu.add(0, 13, 13, "Yer aç")
            menu.add(0, 8, 13, "Müzik")
            menu.add(0, 9, 14, "Güncellemeyi kontrol et")
            setOnMenuItemClickListener { item ->
                when (item.itemId) {
                    1 -> {
                        driveSection = DriveSection.STARRED
                        styleDriveNav()
                        vm.showStarred()
                    }
                    2 -> {
                        driveSection = DriveSection.HOME
                        styleDriveNav()
                        vm.showRecent()
                    }
                    14 -> {
                        driveSection = DriveSection.TRASH
                        styleDriveNav()
                        vm.showTrash()
                    }
                    3 -> showOfflineDownloads()
                    4 -> {
                        val first = vm.state.value.files.firstOrNull()
                        if (first != null) vm.enterSelection(first.id)
                        else Toast.makeText(this@MainActivity, "Önce dosya listesi gerekli", Toast.LENGTH_SHORT).show()
                    }
                    5 -> picker.launch("*/*")
                    6 -> promptNewFolder()
                    11 -> {
                        val plate = currentIntakePlate()
                        if (plate != null) {
                            openIntakeForPlate(plate, vm.state.value.crumbs.last().id)
                        }
                    }
                    10 -> openVehicleIntake()
                    12 -> startActivity(Intent(this@MainActivity, PhotosLibraryActivity::class.java))
                    15 -> startActivity(Intent(this@MainActivity, CallsActivity::class.java))
                    7 -> startActivity(Intent(this@MainActivity, BackupSettingsActivity::class.java))
                    16 -> openPermissionWizard()
                    13 -> startActivity(Intent(this@MainActivity, FreeUpSpaceActivity::class.java))
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

    private fun applyFilesLayout() {
        adapter.setGridMode(gridLayout)
        list.layoutManager = if (gridLayout) {
            GridLayoutManager(this, 3)
        } else {
            LinearLayoutManager(this)
        }
        findViewById<Button>(R.id.btnLayout).text =
            if (gridLayout) "☰ Liste" else "▦ Izgara"
        findViewById<Button>(R.id.btnLayout).setTextColor(
            ContextCompat.getColor(this, R.color.tr_ink),
        )
    }

    private fun openVehicleIntake() {
        val plate = session.lastIntakePlate
        if (plate.isNotBlank()) {
            startActivity(
                VehicleIntakeActivity.intent(
                    this,
                    plate,
                    session.lastIntakeFolderId.takeIf { it.isNotBlank() },
                ),
            )
        } else {
            startActivity(Intent(this, VehicleIntakeActivity::class.java))
        }
    }

    private fun openIntakeForPlate(plate: String, folderId: String? = null) {
        startActivity(VehicleIntakeActivity.intent(this, plate, folderId))
    }

    /** Plaka klasörü içindeyken (TR Araç Kabul / PLAKA) plaka adını döner. */
    private fun currentIntakePlate(): String? {
        val c = vm.state.value.crumbs
        if (c.size < 2) return null
        if (!c[c.size - 2].name.equals(INTAKE_ROOT, ignoreCase = true)) return null
        return c.last().name
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

    private fun promptRename(entry: FileEntry) {
        val input = EditText(this).apply {
            setText(entry.name)
            setSelection(entry.name.length)
        }
        AlertDialog.Builder(this)
            .setTitle("Yeniden adlandır")
            .setView(input)
            .setPositiveButton("Kaydet") { _, _ ->
                vm.renameEntry(entry, input.text.toString())
            }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun promptMove(entry: FileEntry) {
        val folders = vm.state.value.files.filter { it.kind == "folder" && it.id != entry.id }
        val labels = mutableListOf("Dosyalarım (kök)")
        val targets = mutableListOf(session.storageRootId.ifBlank { "" })
        folders.forEach {
            labels.add(it.name)
            targets.add(it.id)
        }
        if (labels.size == 1 && targets[0].isBlank()) {
            Toast.makeText(this, "Taşımak için hedef klasör yok — önce bir klasör oluşturun", Toast.LENGTH_LONG).show()
        }
        AlertDialog.Builder(this)
            .setTitle("Taşı: ${entry.name}")
            .setItems(labels.toTypedArray()) { _, which ->
                vm.moveEntry(entry, targets[which])
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
            driveSection = DriveSection.FILES
            styleDriveNav()
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
        if (mime == "application/pdf" || entry.name.endsWith(".pdf", ignoreCase = true)) {
            vm.previewPdf(entry)
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
        const val INTAKE_ROOT = "TR Araç Kabul"

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
            MimeHelper.guessFromExtension(ext)?.let { return it }
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
    private val onRename: (FileEntry) -> Unit,
    private val onMove: (FileEntry) -> Unit,
    private val onToggleCheck: (FileEntry) -> Unit,
    private val onAddIntakePhotos: (FileEntry) -> Unit,
    private val onRestore: (FileEntry) -> Unit,
    private val onPurge: (FileEntry) -> Unit,
) : RecyclerView.Adapter<FileAdapter.VH>() {
    private var items: List<FileEntry> = emptyList()
    private var selectionMode: Boolean = false
    private var selectedIds: Set<String> = emptySet()
    private var token: String = ""
    private var serverUrl: String = ""
    private var pathHint: String = ""
    private var gridMode: Boolean = false
    private var trashMode: Boolean = false

    fun setGridMode(grid: Boolean) {
        if (gridMode == grid) return
        gridMode = grid
        notifyDataSetChanged()
    }

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

    fun setTrashMode(trash: Boolean) {
        if (trashMode == trash) return
        trashMode = trash
        notifyDataSetChanged()
    }

    override fun getItemViewType(position: Int): Int = if (gridMode) 1 else 0

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
        val layout = if (viewType == 1) R.layout.item_file_grid else R.layout.item_file
        val view = LayoutInflater.from(parent.context).inflate(layout, parent, false)
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
                if (trashMode) {
                    menu.add(0, 1, 1, "Geri yükle")
                    menu.add(0, 2, 2, "Kalıcı sil")
                    setOnMenuItemClickListener { mi ->
                        when (mi.itemId) {
                            1 -> onRestore(item)
                            2 -> onPurge(item)
                        }
                        true
                    }
                    show()
                    return@setOnClickListener
                }
                if (item.kind == "folder" && isIntakePlateFolderRow(pathHint)) {
                    menu.add(0, 10, 0, "Fotoğraf ekle")
                }
                if (item.kind == "file") menu.add(0, 1, 1, "İndir")
                menu.add(0, 2, 2, "Paylaş")
                menu.add(0, 3, 3, if (item.starred) "Yıldızı kaldır" else "Yıldızla")
                menu.add(0, 5, 4, "Yeniden adlandır")
                menu.add(0, 6, 5, "Taşı")
                menu.add(0, 4, 6, "Sil")
                setOnMenuItemClickListener { mi ->
                    when (mi.itemId) {
                        10 -> onAddIntakePhotos(item)
                        1 -> onDownload(item)
                        2 -> onShare(item)
                        3 -> onStar(item)
                        5 -> onRename(item)
                        6 -> onMove(item)
                        4 -> onDelete(item)
                    }
                    true
                }
                show()
            }
        }

        holder.thumb.setImageDrawable(null)
        holder.thumb.setBackgroundColor(Color.parseColor("#E8F2FC"))
        val thumbSize = if (gridMode) 480 else 96
        val looksImage = mime.startsWith("image/") ||
            MainActivity.mimeFromFileName(item.name).startsWith("image/")
        when {
            item.kind == "folder" -> {
                holder.thumb.setBackgroundColor(Color.parseColor("#0B5CAD"))
                if (gridMode) {
                    holder.thumb.setImageResource(android.R.drawable.ic_menu_agenda)
                    holder.thumb.setColorFilter(Color.WHITE)
                } else {
                    holder.thumb.clearColorFilter()
                }
            }
            looksImage && token.isNotBlank() && serverUrl.isNotBlank() -> {
                holder.thumb.clearColorFilter()
                val url = "$serverUrl/api/files/download/${item.id}?inline=1"
                holder.thumb.load(url) {
                    headers(Headers.headersOf("Authorization", "Bearer $token"))
                    size(thumbSize, thumbSize)
                    crossfade(true)
                    allowHardware(true)
                }
            }
            mime == "application/pdf" || item.name.endsWith(".pdf", ignoreCase = true) -> {
                holder.thumb.clearColorFilter()
                holder.thumb.setBackgroundColor(Color.parseColor("#FDE8E8"))
            }
            mime.startsWith("video/") -> {
                holder.thumb.clearColorFilter()
                holder.thumb.setBackgroundColor(Color.parseColor("#1F2937"))
            }
            else -> holder.thumb.clearColorFilter()
        }

        val badge = when {
            item.starred -> "Yıldızlı"
            item.kind == "folder" && isIntakePlateFolderRow(pathHint) -> "Araç kabul"
            mime == "application/pdf" -> "PDF"
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

    private fun isIntakePlateFolderRow(pathHint: String): Boolean {
        return pathHint.endsWith(MainActivity.INTAKE_ROOT, ignoreCase = true)
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
