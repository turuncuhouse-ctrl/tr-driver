package net.neciparmagan.trdriver

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.MediaStore
import android.text.Editable
import android.text.TextWatcher
import android.text.format.Formatter
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.PopupMenu
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.IntentSenderRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.DiffUtil
import androidx.recyclerview.widget.GridLayoutManager
import androidx.recyclerview.widget.RecyclerView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.LocalMedia
import net.neciparmagan.trdriver.data.MediaAlbum
import net.neciparmagan.trdriver.data.MediaAccess
import net.neciparmagan.trdriver.data.MediaCatalog
import net.neciparmagan.trdriver.data.MediaThumbLoader
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadConflictPolicy
import net.neciparmagan.trdriver.data.UploadConflictUi
import net.neciparmagan.trdriver.data.UploadQueueDb
import net.neciparmagan.trdriver.upload.UploadForegroundService
import java.text.SimpleDateFormat
import java.util.ArrayDeque
import java.util.Calendar
import java.util.Date
import java.util.Locale

/**
 * Android Gallery / Google Photos style library:
 * timeline with date sections, albums, cloud, search, multi-select.
 */
class PhotosLibraryActivity : AppCompatActivity() {
    private enum class Tab { PHOTOS, ALBUMS, CLOUD }
    private enum class MediaFilter { ALL, PHOTOS, VIDEOS }
    private enum class TimelineZoom { DAY, MONTH, YEAR }

    private sealed class TimelineItem {
        data class Header(val label: String, val dayKey: String) : TimelineItem()
        data class Photo(val media: LocalMedia) : TimelineItem()
        data class Cloud(val entry: FileEntry) : TimelineItem()
    }

    private lateinit var session: SessionStore
    private lateinit var api: DriveApi
    private lateinit var title: TextView
    private lateinit var subtitle: TextView
    private lateinit var progress: ProgressBar
    private lateinit var grid: RecyclerView
    private lateinit var search: EditText
    private lateinit var selectionBar: LinearLayout
    private lateinit var selectionBarScroll: HorizontalScrollView
    private lateinit var selectionCount: TextView
    private lateinit var galleryBottomNav: View
    private lateinit var stickyHeader: TextView
    private lateinit var tabPhotos: Button
    private lateinit var tabAlbums: Button
    private lateinit var tabCloud: Button
    private lateinit var btnSelect: Button
    private lateinit var filterAll: Button
    private lateinit var filterPhotos: Button
    private lateinit var filterVideos: Button
    private lateinit var zoomDay: Button
    private lateinit var zoomMonth: Button
    private lateinit var zoomYear: Button

    private var tab = Tab.PHOTOS
    private var mediaFilter = MediaFilter.ALL
    private var timelineZoom = TimelineZoom.DAY
    private var viewingAlbum: MediaAlbum? = null
    private var viewingCloudAlbum: FileEntry? = null
    private var showingCloudAlbumList = false
    private var selectionMode = false
    private var searchQuery = ""
    private var searchJob: Job? = null
    private var launchedAsGalleryApp = false
    private var pickMode = false
    private var uploadedKeys: Set<String> = emptySet()

    private var allLocal: List<LocalMedia> = emptyList()
    private var allAlbums: List<MediaAlbum> = emptyList()
    private var allCloud: List<FileEntry> = emptyList()

    private val selectedLocal = linkedSetOf<String>() // mediaKey
    private val selectedCloud = linkedSetOf<String>() // file id
    private var pendingLocalDelete: List<LocalMedia> = emptyList()

    private val timelineAdapter = TimelineAdapter(
        onOpenLocal = { if (selectionMode) toggleLocal(it) else openLocal(it) },
        onLongLocal = { media, _ -> beginMultiSelectLocal(media) },
        onOpenCloud = { if (selectionMode) toggleCloud(it) else openCloud(it) },
        onLongCloud = { entry, _ -> beginMultiSelectCloud(entry) },
        isLocalSelected = { selectedLocal.contains(it.mediaKey) },
        isCloudSelected = { selectedCloud.contains(it.id) },
        selectionActive = { selectionMode },
        isBackedUp = { uploadedKeys.contains(it.mediaKey) },
    )
    private val albumAdapter = AlbumGridAdapter { openAlbum(it) }

    private val permissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { result ->
            if (result.values.any { it }) reload()
            else Toast.makeText(this, "Galeri izni gerekli", Toast.LENGTH_LONG).show()
            finishBootstrapAfterPermissions()
        }

    private val deleteRequestLauncher =
        registerForActivityResult(ActivityResultContracts.StartIntentSenderForResult()) { result ->
            if (result.resultCode == Activity.RESULT_OK) {
                Toast.makeText(this, "${pendingLocalDelete.size} öğe silindi", Toast.LENGTH_SHORT).show()
                exitSelection()
                reload()
            } else {
                Toast.makeText(this, "Silme iptal", Toast.LENGTH_SHORT).show()
            }
            pendingLocalDelete = emptyList()
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_photos_library)
        session = SessionStore(this)
        api = DriveApi(session, this)

        title = findViewById(R.id.photosTitle)
        subtitle = findViewById(R.id.photosSubtitle)
        progress = findViewById(R.id.photosProgress)
        grid = findViewById(R.id.photosGrid)
        search = findViewById(R.id.photosSearch)
        selectionBar = findViewById(R.id.selectionBar)
        selectionBarScroll = findViewById(R.id.selectionBarScroll)
        selectionCount = findViewById(R.id.selectionCount)
        galleryBottomNav = findViewById(R.id.galleryBottomNav)
        stickyHeader = findViewById(R.id.stickyTimelineHeader)
        tabPhotos = findViewById(R.id.tabPhotos)
        tabAlbums = findViewById(R.id.tabAlbums)
        tabCloud = findViewById(R.id.tabCloud)
        btnSelect = findViewById(R.id.btnPhotosSelect)
        filterAll = findViewById(R.id.filterAll)
        filterPhotos = findViewById(R.id.filterPhotos)
        filterVideos = findViewById(R.id.filterVideos)
        zoomDay = findViewById(R.id.zoomDay)
        zoomMonth = findViewById(R.id.zoomMonth)
        zoomYear = findViewById(R.id.zoomYear)

        findViewById<Button>(R.id.btnOpenDrive).setOnClickListener {
            startActivity(
                Intent(this, MainActivity::class.java).apply {
                    flags = Intent.FLAG_ACTIVITY_REORDER_TO_FRONT or Intent.FLAG_ACTIVITY_CLEAR_TOP
                },
            )
        }

        pickMode = intent?.action == Intent.ACTION_PICK ||
            intent?.action == Intent.ACTION_GET_CONTENT
        launchedAsGalleryApp = !pickMode && (
            intent?.action == Intent.ACTION_MAIN ||
                intent?.hasCategory(Intent.CATEGORY_LAUNCHER) == true ||
                intent?.hasCategory(Intent.CATEGORY_APP_GALLERY) == true
            )
        val closeBtn = findViewById<Button>(R.id.btnPhotosClose)
        if (pickMode) {
            title.text = "Fotoğraf seç"
            closeBtn.text = "İptal"
            closeBtn.visibility = View.VISIBLE
            btnSelect.visibility = View.GONE
        } else if (launchedAsGalleryApp) {
            title.setText(R.string.gallery_launcher_name)
            // Üste yanlışlıkla "Kapat" basılmasın — geri / menü kullan
            closeBtn.visibility = View.GONE
        } else {
            closeBtn.text = "Geri"
            closeBtn.visibility = View.VISIBLE
        }

        applyTimelineLayout()
        timelineAdapter.updateAuth(session.token.orEmpty(), session.serverUrl)
        albumAdapter.updateAuth(session.token.orEmpty(), session.serverUrl)
        grid.addOnScrollListener(object : RecyclerView.OnScrollListener() {
            override fun onScrolled(recyclerView: RecyclerView, dx: Int, dy: Int) {
                updateStickyHeader()
            }
        })

        // Bottom nav (Google Photos style)
        findViewById<Button>(R.id.navPhotos).setOnClickListener { selectTab(Tab.PHOTOS) }
        findViewById<Button>(R.id.navAlbums).setOnClickListener { selectTab(Tab.ALBUMS) }
        findViewById<Button>(R.id.navLibrary).setOnClickListener { selectTab(Tab.CLOUD) }
        tabPhotos.setOnClickListener { selectTab(Tab.PHOTOS) }
        tabAlbums.setOnClickListener { selectTab(Tab.ALBUMS) }
        tabCloud.setOnClickListener { selectTab(Tab.CLOUD) }
        filterAll.setOnClickListener { setMediaFilter(MediaFilter.ALL) }
        filterPhotos.setOnClickListener { setMediaFilter(MediaFilter.PHOTOS) }
        filterVideos.setOnClickListener { setMediaFilter(MediaFilter.VIDEOS) }
        zoomDay.setOnClickListener { setTimelineZoom(TimelineZoom.DAY) }
        zoomMonth.setOnClickListener { setTimelineZoom(TimelineZoom.MONTH) }
        zoomYear.setOnClickListener { setTimelineZoom(TimelineZoom.YEAR) }
        styleFilters()
        styleZoom()
        btnSelect.setOnClickListener {
            if (selectionMode) exitSelection() else enterSelection()
        }
        findViewById<Button>(R.id.btnPhotosMore).setOnClickListener { showMoreMenu(it) }
        findViewById<Button>(R.id.btnPhotosClose).setOnClickListener {
            if (pickMode) {
                setResult(Activity.RESULT_CANCELED)
                finish()
            } else {
                finish()
            }
        }
        findViewById<Button>(R.id.btnSelectAll).setOnClickListener { selectAllVisible() }
        findViewById<Button>(R.id.btnShareSelected).setOnClickListener { shareSelected() }
        findViewById<Button>(R.id.btnUploadSelected).setOnClickListener { uploadSelectedToCloud() }
        findViewById<Button>(R.id.btnOpenSelected).setOnClickListener { openSelectedExternal() }
        findViewById<Button>(R.id.btnInfoSelected).setOnClickListener { showSelectedInfo() }
        findViewById<Button>(R.id.btnDeleteSelected).setOnClickListener { confirmDeleteSelected() }
        findViewById<Button>(R.id.btnCancelSelect).setOnClickListener { exitSelection() }

        search.setOnEditorActionListener { _, actionId, _ ->
            if (actionId == android.view.inputmethod.EditorInfo.IME_ACTION_SEARCH) {
                searchJob?.cancel()
                searchQuery = search.text?.toString()?.trim().orEmpty()
                applyFilter()
                val imm = getSystemService(INPUT_METHOD_SERVICE) as? android.view.inputmethod.InputMethodManager
                imm?.hideSoftInputFromWindow(search.windowToken, 0)
                true
            } else false
        }
        search.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit
            override fun afterTextChanged(s: Editable?) {
                searchJob?.cancel()
                searchJob = lifecycleScope.launch {
                    delay(200)
                    searchQuery = s?.toString()?.trim().orEmpty()
                    applyFilter()
                }
            }
        })

        onBackPressedDispatcher.addCallback(this, object : androidx.activity.OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                when {
                    selectionMode -> exitSelection()
                    viewingAlbum != null -> {
                        viewingAlbum = null
                        exitSelection(refresh = false)
                        loadAlbums()
                    }
                    viewingCloudAlbum != null -> {
                        viewingCloudAlbum = null
                        exitSelection(refresh = false)
                        loadCloudAlbums()
                    }
                    showingCloudAlbumList -> {
                        showingCloudAlbumList = false
                        exitSelection(refresh = false)
                        loadCloudTimeline()
                    }
                    else -> {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                    }
                }
            }
        })

        if (!AppBootstrap.isBootstrapped(this)) {
            val needed = AppBootstrap.neededPermissions(this)
            if (needed.isNotEmpty()) {
                permissionLauncher.launch(needed)
            } else {
                finishBootstrapAfterPermissions()
            }
        } else if (!hasMediaPermission()) {
            permissionLauncher.launch(mediaPermissions())
        } else if (intent?.action == Intent.ACTION_VIEW && intent.data != null) {
            val uri = intent.data!!
            val name = intent.getStringExtra(Intent.EXTRA_TITLE)
                ?: uri.lastPathSegment
                ?: "medya"
            val mime = contentResolver.getType(uri)
                ?: intent.type
                ?: MediaPreviewActivity.guessMimeFromName(name)
            startActivity(
                Intent(this, MediaPreviewActivity::class.java).apply {
                    putExtra(MediaPreviewActivity.EXTRA_NAME, name)
                    putExtra(MediaPreviewActivity.EXTRA_MIME, mime)
                    putExtra(MediaPreviewActivity.EXTRA_LOCAL_URI, uri.toString())
                },
            )
            selectTab(Tab.PHOTOS)
        } else {
            selectTab(Tab.PHOTOS)
        }

        // Keep session / Wi‑Fi backup alive while gallery is used.
        if (session.isLoggedIn && session.galleryBackupEnabled) {
            net.neciparmagan.trdriver.backup.GalleryBackupWorker.schedule(this)
            net.neciparmagan.trdriver.backup.TrKeepAliveService.startIfNeeded(this)
        }
        refreshGallerySubtitle()
    }

    private fun finishBootstrapAfterPermissions() {
        if (!AppBootstrap.isBootstrapped(this)) {
            AppBootstrap.maybePinGalleryShortcut(this)
            if (!AppBootstrap.requestGalleryRole(this)) {
                AppBootstrap.maybeOfferDefaultGallery(this)
            }
            AppBootstrap.markBootstrapped(this)
        }
        if (intent?.action == Intent.ACTION_VIEW && intent.data != null && hasMediaPermission()) {
            // handled only on cold start path; reload timeline
        }
        selectTab(Tab.PHOTOS)
    }

    private fun refreshGallerySubtitle() {
        val login = if (session.isLoggedIn) {
            session.email.ifBlank { "oturum açık" }
        } else {
            "giriş yok (yerel galeri)"
        }
        val backup = when {
            !session.isLoggedIn -> ""
            session.galleryBackupEnabled -> " · Wi‑Fi yedek açık"
            else -> " · yedek kapalı"
        }
        subtitle.text = "$login$backup"
    }

    override fun onStart() {
        super.onStart()
        UploadConflictUi.bindActivity(this)
    }

    override fun onResume() {
        super.onResume()
        if (session.isLoggedIn && session.galleryBackupEnabled) {
            net.neciparmagan.trdriver.backup.GalleryBackupWorker.schedule(this)
        }
        refreshBackupChip()
        refreshUploadedKeys()
    }

    override fun onStop() {
        UploadConflictUi.unbindActivity()
        super.onStop()
    }

    private fun selectTab(next: Tab) {
        tab = next
        viewingAlbum = null
        viewingCloudAlbum = null
        showingCloudAlbumList = false
        exitSelection(refresh = false)
        search.setText("")
        searchQuery = ""
        styleTabs()
        reload()
    }

    private fun styleTabs() {
        fun style(btn: Button, active: Boolean) {
            btn.setTypeface(null, if (active) Typeface.BOLD else Typeface.NORMAL)
            btn.alpha = if (active) 1f else 0.7f
            btn.setTextColor(
                ContextCompat.getColor(this, if (active) R.color.tr_blue else R.color.tr_ink),
            )
        }
        style(tabPhotos, tab == Tab.PHOTOS)
        style(tabAlbums, tab == Tab.ALBUMS)
        style(tabCloud, tab == Tab.CLOUD)
        findViewById<Button>(R.id.navPhotos).let { style(it, tab == Tab.PHOTOS) }
        findViewById<Button>(R.id.navAlbums).let { style(it, tab == Tab.ALBUMS) }
        findViewById<Button>(R.id.navLibrary).let { style(it, tab == Tab.CLOUD) }
        findViewById<View>(R.id.filterScroll).visibility =
            when (tab) {
                Tab.PHOTOS -> View.VISIBLE
                Tab.CLOUD -> if (showingCloudAlbumList) View.GONE else View.VISIBLE
                Tab.ALBUMS -> if (viewingAlbum != null) View.VISIBLE else View.GONE
            }
        refreshBackupChip()
    }

    private fun refreshBackupChip() {
        val chip = findViewById<TextView>(R.id.galleryBackupChip)
        if (!session.isLoggedIn) {
            chip.visibility = View.GONE
            return
        }
        chip.visibility = View.VISIBLE
        chip.text = session.backupStatusLine()
        chip.setOnClickListener {
            startActivity(Intent(this, BackupSettingsActivity::class.java))
        }
    }

    private fun showEmpty(titleText: String, body: String) {
        findViewById<View>(R.id.galleryEmpty).visibility = View.VISIBLE
        findViewById<TextView>(R.id.galleryEmptyTitle).text = titleText
        findViewById<TextView>(R.id.galleryEmptyBody).text = body
        grid.visibility = View.GONE
    }

    private fun hideEmpty() {
        findViewById<View>(R.id.galleryEmpty).visibility = View.GONE
        grid.visibility = View.VISIBLE
    }

    private fun setMediaFilter(next: MediaFilter) {
        mediaFilter = next
        styleFilters()
        applyFilter()
    }

    private fun styleFilters() {
        fun style(btn: Button, active: Boolean) {
            btn.setTypeface(null, if (active) Typeface.BOLD else Typeface.NORMAL)
            btn.setTextColor(
                ContextCompat.getColor(this, if (active) R.color.tr_blue else R.color.tr_ink),
            )
            btn.alpha = 1f
        }
        style(filterAll, mediaFilter == MediaFilter.ALL)
        style(filterPhotos, mediaFilter == MediaFilter.PHOTOS)
        style(filterVideos, mediaFilter == MediaFilter.VIDEOS)
    }

    private fun setTimelineZoom(next: TimelineZoom) {
        timelineZoom = next
        styleZoom()
        applyTimelineLayout()
        applyFilter()
    }

    private fun styleZoom() {
        fun style(btn: Button, active: Boolean) {
            btn.setTypeface(null, if (active) Typeface.BOLD else Typeface.NORMAL)
            btn.setTextColor(
                ContextCompat.getColor(this, if (active) R.color.tr_blue else R.color.tr_ink),
            )
        }
        style(zoomDay, timelineZoom == TimelineZoom.DAY)
        style(zoomMonth, timelineZoom == TimelineZoom.MONTH)
        style(zoomYear, timelineZoom == TimelineZoom.YEAR)
    }

    private fun currentSpanCount(): Int = when (timelineZoom) {
        TimelineZoom.DAY -> 3
        TimelineZoom.MONTH -> 5
        TimelineZoom.YEAR -> 7
    }

    private fun applyTimelineLayout() {
        val spans = currentSpanCount()
        val glm = GridLayoutManager(this, spans)
        glm.spanSizeLookup = object : GridLayoutManager.SpanSizeLookup() {
            override fun getSpanSize(position: Int): Int {
                return if (grid.adapter === timelineAdapter && timelineAdapter.isHeader(position)) {
                    spans
                } else {
                    1
                }
            }
        }
        grid.layoutManager = glm
    }

    private fun updateStickyHeader() {
        if (grid.adapter !== timelineAdapter || tab == Tab.ALBUMS) {
            stickyHeader.visibility = View.GONE
            return
        }
        val lm = grid.layoutManager as? GridLayoutManager ?: return
        val first = lm.findFirstVisibleItemPosition()
        if (first == RecyclerView.NO_POSITION) {
            stickyHeader.visibility = View.GONE
            return
        }
        val label = timelineAdapter.headerLabelAtOrBefore(first)
        if (label.isNullOrBlank()) {
            stickyHeader.visibility = View.GONE
        } else {
            stickyHeader.visibility = View.VISIBLE
            stickyHeader.text = label
        }
    }

    private fun beginMultiSelectLocal(media: LocalMedia) {
        if (!selectionMode) enterSelection()
        if (media.mediaKey !in selectedLocal) {
            selectedLocal.add(media.mediaKey)
        }
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
        Toast.makeText(this, "Toplu seçim · alt çubuktan işlem yapın", Toast.LENGTH_SHORT).show()
    }

    private fun beginMultiSelectCloud(entry: FileEntry) {
        if (entry.kind == "folder") {
            openCloud(entry)
            return
        }
        if (!selectionMode) enterSelection()
        if (entry.id !in selectedCloud) {
            selectedCloud.add(entry.id)
        }
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
        Toast.makeText(this, "Toplu seçim · alt çubuktan işlem yapın", Toast.LENGTH_SHORT).show()
    }

    private fun refreshUploadedKeys() {
        lifecycleScope.launch {
            uploadedKeys = withContext(Dispatchers.IO) {
                net.neciparmagan.trdriver.data.UploadedMediaDb(this@PhotosLibraryActivity).allUploadedKeys()
            }
            if (grid.adapter === timelineAdapter) {
                timelineAdapter.notifyDataSetChanged()
            }
        }
    }

    private fun showMoreMenu(anchor: View) {
        PopupMenu(this, anchor).apply {
            menu.add(0, 1, 0, "Yer aç (yedeklenenler)")
            menu.add(0, 2, 1, "Yedek ayarları")
            menu.add(0, 3, 2, "Şimdi yedekle")
            menu.add(0, 4, 3, "TR Drive dosyaları")
            menu.add(0, 5, 4, "Seçim modu")
            menu.add(0, 6, 5, "Ana ekrana TR Galeri ekle")
            setOnMenuItemClickListener { item ->
                when (item.itemId) {
                    1 -> startActivity(Intent(this@PhotosLibraryActivity, FreeUpSpaceActivity::class.java))
                    2 -> startActivity(Intent(this@PhotosLibraryActivity, BackupSettingsActivity::class.java))
                    3 -> {
                        if (!session.isLoggedIn) {
                            Toast.makeText(this@PhotosLibraryActivity, "Giriş gerekli", Toast.LENGTH_SHORT).show()
                        } else {
                            net.neciparmagan.trdriver.backup.GalleryBackupWorker.runNow(this@PhotosLibraryActivity)
                            Toast.makeText(this@PhotosLibraryActivity, "Yedekleme başlatıldı", Toast.LENGTH_SHORT).show()
                        }
                    }
                    4 -> startActivity(
                        Intent(this@PhotosLibraryActivity, MainActivity::class.java).apply {
                            flags = Intent.FLAG_ACTIVITY_REORDER_TO_FRONT or Intent.FLAG_ACTIVITY_CLEAR_TOP
                        },
                    )
                    5 -> if (selectionMode) exitSelection() else enterSelection()
                    6 -> offerGalleryHomeShortcut()
                }
                true
            }
            show()
        }
    }

    private fun offerGalleryHomeShortcut() {
        val shortcut = androidx.core.content.pm.ShortcutInfoCompat.Builder(this, "tr_galeri_home")
            .setShortLabel(getString(R.string.gallery_launcher_name))
            .setLongLabel(getString(R.string.gallery_launcher_name))
            .setIcon(androidx.core.graphics.drawable.IconCompat.createWithResource(this, R.drawable.ic_gallery_app))
            .setIntent(
                Intent(this, PhotosLibraryActivity::class.java).apply {
                    action = Intent.ACTION_MAIN
                    addCategory(Intent.CATEGORY_LAUNCHER)
                    flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
                },
            )
            .build()
        val ok = androidx.core.content.pm.ShortcutManagerCompat.requestPinShortcut(this, shortcut, null)
        Toast.makeText(
            this,
            if (ok) "Ana ekrana ekleme isteği gönderildi" else "Bu cihazda kısayol eklenemiyor — uygulama listesinden TR Galeri’yi bulun",
            Toast.LENGTH_LONG,
        ).show()
    }

    private fun reload() {
        when (tab) {
            Tab.PHOTOS -> loadAllPhotos()
            Tab.ALBUMS -> loadAlbums()
            Tab.CLOUD -> loadCloudTimeline()
        }
    }

    private fun loadAllPhotos() {
        title.text = if (launchedAsGalleryApp) getString(R.string.gallery_launcher_name) else "TR Photos"
        subtitle.text = "Tüm fotoğraf ve videolar · tarihe göre"
        hideEmpty()
        if (!hasMediaPermission()) {
            subtitle.text = "Galeri izni gerekli"
            showEmpty("Galeri izni gerekli", "Fotoğraf ve videoları görmek için izin verin.")
            return
        }
        progress.visibility = View.VISIBLE
        grid.adapter = timelineAdapter
        applyTimelineLayout()
        lifecycleScope.launch {
            allLocal = withContext(Dispatchers.IO) { MediaCatalog.scan(this@PhotosLibraryActivity, 4000) }
            refreshUploadedKeys()
            applyFilter()
            progress.visibility = View.GONE
            refreshBackupChip()
        }
    }

    private fun loadAlbums() {
        title.text = "Albümler"
        subtitle.text = "Cihaz klasörleri · Google Kitaplık tarzı"
        hideEmpty()
        if (!hasMediaPermission()) {
            subtitle.text = "Galeri izni gerekli"
            showEmpty("Galeri izni gerekli", "Albümleri görmek için fotoğraf/video izni verin.")
            return
        }
        progress.visibility = View.VISIBLE
        grid.adapter = albumAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 2
        lifecycleScope.launch {
            allAlbums = withContext(Dispatchers.IO) { MediaCatalog.listAlbums(this@PhotosLibraryActivity) }
            applyFilter()
            progress.visibility = View.GONE
            refreshBackupChip()
        }
    }

    private fun openAlbum(album: MediaAlbum) {
        if (album.id.startsWith("cloud:")) {
            val id = album.id.removePrefix("cloud:")
            viewingCloudAlbum = FileEntry(id = id, name = album.name, kind = "folder")
            loadCloudAlbumContents(viewingCloudAlbum!!)
            return
        }
        viewingAlbum = album
        title.text = album.name
        subtitle.text = "${album.count} öğe · tarihe göre"
        progress.visibility = View.VISIBLE
        hideEmpty()
        grid.adapter = timelineAdapter
        applyTimelineLayout()
        lifecycleScope.launch {
            var items = withContext(Dispatchers.IO) {
                MediaCatalog.scanAlbum(this@PhotosLibraryActivity, album.id)
            }
            // Fallback: some OEMs return empty bucket queries — use full scan filter.
            if (items.isEmpty() && album.count > 0) {
                items = withContext(Dispatchers.IO) {
                    MediaCatalog.scan(this@PhotosLibraryActivity, 4000)
                        .filter { it.albumId == album.id }
                }
            }
            if (items.isEmpty() && album.coverUri != null) {
                // At least show the cover item so the album is never a blank black grid.
                val cover = album.coverUri
                items = withContext(Dispatchers.IO) {
                    MediaCatalog.scan(this@PhotosLibraryActivity, 4000)
                        .filter { it.uri == cover || it.albumName == album.name }
                        .ifEmpty {
                            listOf(
                                LocalMedia(
                                    mediaKey = cover.toString(),
                                    uri = cover,
                                    displayName = album.name,
                                    mimeType = contentResolver.getType(cover) ?: "image/jpeg",
                                    sizeBytes = 0L,
                                    dateTakenMs = System.currentTimeMillis(),
                                    isVideo = (contentResolver.getType(cover) ?: "").startsWith("video/"),
                                    albumName = album.name,
                                    albumId = album.id,
                                ),
                            )
                        }
                }
            }
            allLocal = items
            applyFilter()
            progress.visibility = View.GONE
            if (items.isEmpty()) {
                showEmpty(
                    "Albüm boş görünüyor",
                    "Bu klasörde medya okunamadı. Galeri iznini tam verin veya Wi‑Fi yedek ile eşitlemeyi bekleyin.",
                )
            }
        }
    }

    private fun loadCloudTimeline() {
        if (!session.isLoggedIn) {
            Toast.makeText(this, "Bulut için giriş gerekli", Toast.LENGTH_LONG).show()
            return
        }
        viewingCloudAlbum = null
        showingCloudAlbumList = false
        title.text = "Kitaplık"
        subtitle.text = "Bulut yedekleri · gün / ay / yıl"
        hideEmpty()
        subtitle.setOnClickListener { loadCloudAlbums() }
        progress.visibility = View.VISIBLE
        grid.adapter = timelineAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            try {
                allCloud = withContext(Dispatchers.IO) { api.listCloudPhotosFlat(1200) }
                applyFilter()
                styleTabs()
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun loadCloudAlbums() {
        viewingCloudAlbum = null
        showingCloudAlbumList = true
        styleTabs()
        title.text = "Bulut albümleri"
        subtitle.text = "Cihaz klasörleri · zaman çizelgesi için Bulut sekmesi"
        subtitle.setOnClickListener(null)
        progress.visibility = View.VISIBLE
        grid.adapter = albumAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 2
        lifecycleScope.launch {
            try {
                val folders = withContext(Dispatchers.IO) { api.listCloudPhotoAlbums() }
                allAlbums = withContext(Dispatchers.IO) {
                    folders.map { folder ->
                        val kids = runCatching { api.listFiles(folder.id) }.getOrDefault(emptyList())
                        val mediaKids = kids.filter { it.kind != "folder" }
                        val cover = mediaKids.firstOrNull { !MainActivity.resolveMime(it).startsWith("video/") }
                            ?: mediaKids.firstOrNull()
                        MediaAlbum(
                            id = "cloud:${folder.id}",
                            name = folder.name,
                            count = mediaKids.size,
                            coverUri = null,
                            coverRemoteId = cover?.id,
                        )
                    }
                }
                applyFilter()
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun loadCloudAlbumContents(folder: FileEntry) {
        showingCloudAlbumList = false
        styleTabs()
        title.text = folder.name
        subtitle.text = "Bulut albümü · gün bazında"
        progress.visibility = View.VISIBLE
        grid.adapter = timelineAdapter
        applyTimelineLayout()
        lifecycleScope.launch {
            try {
                data class Node(val id: String, val year: Int?, val month: Int?)
                val out = ArrayList<FileEntry>()
                val queue = ArrayDeque<Node>()
                // Folder name may itself be year/device; start without hints.
                queue.add(Node(folder.id, folder.name.toIntOrNull()?.takeIf { it in 1970..2100 }, null))
                var guard = 0
                while (queue.isNotEmpty() && out.size < 1000 && guard < 400) {
                    guard++
                    val node = queue.removeFirst()
                    val children = withContext(Dispatchers.IO) { api.listFiles(node.id) }
                    for (c in children) {
                        if (c.kind == "folder") {
                            val asYear = c.name.toIntOrNull()?.takeIf { it in 1970..2100 }
                            val asMonth = c.name.toIntOrNull()?.takeIf { it in 1..12 && c.name.length <= 2 }
                            queue.add(Node(c.id, asYear ?: node.year, asMonth ?: node.month))
                        } else {
                            out += c.copy(
                                sortTimeMs = net.neciparmagan.trdriver.data.FileEntryDates
                                    .resolveSortMs(c, node.year, node.month),
                            )
                        }
                    }
                }
                allCloud = out.sortedByDescending { it.sortTimeMs }
                applyFilter()
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun applyFilter() {
        val q = searchQuery.lowercase(Locale.getDefault())
        when (tab) {
            Tab.ALBUMS -> {
                if (viewingAlbum == null) {
                    grid.adapter = albumAdapter
                    (grid.layoutManager as GridLayoutManager).spanCount = 2
                    val albums = if (q.isBlank()) allAlbums else allAlbums.filter {
                        it.name.lowercase(Locale.getDefault()).contains(q)
                    }
                    albumAdapter.submit(albums)
                    if (albums.isEmpty()) {
                        showEmpty(
                            if (q.isNotBlank()) "Sonuç yok" else "Albüm yok",
                            if (q.isNotBlank()) "\"$searchQuery\" ile eşleşen albüm bulunamadı."
                            else "Cihazda albüm bulunamadı.",
                        )
                    } else {
                        hideEmpty()
                    }
                    subtitle.text = "${albums.size} albüm" + if (q.isNotBlank()) " · \"$searchQuery\"" else ""
                } else {
                    showLocalTimeline(q)
                }
            }
            Tab.PHOTOS -> showLocalTimeline(q)
            Tab.CLOUD -> {
                if (showingCloudAlbumList) {
                    grid.adapter = albumAdapter
                    (grid.layoutManager as GridLayoutManager).spanCount = 2
                    val albums = if (q.isBlank()) allAlbums else allAlbums.filter {
                        it.name.lowercase(Locale.getDefault()).contains(q)
                    }
                    albumAdapter.submit(albums)
                    if (albums.isEmpty()) {
                        showEmpty("Bulut albümü yok", "Wi‑Fi yedek açıkken albümler burada görünür.")
                    } else {
                        hideEmpty()
                    }
                    subtitle.text = "${albums.size} bulut albümü" + if (q.isNotBlank()) " · \"$searchQuery\"" else ""
                } else {
                    grid.adapter = timelineAdapter
                    applyTimelineLayout()
                    var files = if (q.isBlank()) allCloud else allCloud.filter {
                        it.name.lowercase(Locale.getDefault()).contains(q)
                    }
                    files = when (mediaFilter) {
                        MediaFilter.ALL -> files
                        MediaFilter.PHOTOS -> files.filter {
                            MainActivity.resolveMime(it).startsWith("image/")
                        }
                        MediaFilter.VIDEOS -> files.filter {
                            MainActivity.resolveMime(it).startsWith("video/")
                        }
                    }
                    timelineAdapter.submitCloud(buildCloudTimeline(files))
                    grid.post { updateStickyHeader() }
                    if (files.isEmpty()) {
                        showEmpty(
                            "Kitaplık boş",
                            "Buluta henüz medya yok. Yedekleme ayarlarından Wi‑Fi yedeği açın.",
                        )
                    } else {
                        hideEmpty()
                    }
                    subtitle.text = when {
                        files.isEmpty() -> "Henüz bulutta medya yok · otomatik yedeği açın"
                        q.isNotBlank() -> "${files.size} bulut öğesi · \"$searchQuery\""
                        else -> "${files.size} yedek · gün bazında"
                    }
                }
            }
        }
        refreshSelectionUi()
    }

    private fun showLocalTimeline(q: String) {
        grid.adapter = timelineAdapter
        applyTimelineLayout()
        var media = if (q.isBlank()) allLocal else allLocal.filter { matchesMediaQuery(it, q) }
        media = when (mediaFilter) {
            MediaFilter.ALL -> media
            MediaFilter.PHOTOS -> media.filter { !it.isVideo }
            MediaFilter.VIDEOS -> media.filter { it.isVideo }
        }
        timelineAdapter.submitLocal(buildLocalTimeline(media))
        grid.post { updateStickyHeader() }
        if (media.isEmpty()) {
            stickyHeader.visibility = View.GONE
            showEmpty(
                if (q.isNotBlank()) "Sonuç yok" else "Fotoğraf yok",
                if (q.isNotBlank()) "\"$searchQuery\" ile eşleşen öğe yok."
                else "Telefon galerisinde görüntülenecek öğe yok.",
            )
        } else {
            hideEmpty()
        }
        val backed = media.count { it.mediaKey in uploadedKeys }
        val zoomLabel = when (timelineZoom) {
            TimelineZoom.DAY -> "gün"
            TimelineZoom.MONTH -> "ay"
            TimelineZoom.YEAR -> "yıl"
        }
        val filterLabel = when (mediaFilter) {
            MediaFilter.ALL -> "tümü"
            MediaFilter.PHOTOS -> "fotoğraf"
            MediaFilter.VIDEOS -> "video"
        }
        subtitle.text = "${media.size} öğe · $filterLabel · $zoomLabel · ☁$backed yedekli" +
            if (q.isNotBlank()) " · \"$searchQuery\"" else ""
    }

    /** Token-based search: name, album, mime, video/photo keywords. */
    private fun matchesMediaQuery(item: LocalMedia, rawQuery: String): Boolean {
        val q = rawQuery.trim().lowercase(Locale.getDefault())
        if (q.isEmpty()) return true
        val tokens = q.split(Regex("\\s+")).filter { it.isNotBlank() }
        if (tokens.isEmpty()) return true
        val hay = buildString {
            append(item.displayName.lowercase(Locale.getDefault()))
            append(' ')
            append(item.albumName?.lowercase(Locale.getDefault()).orEmpty())
            append(' ')
            append(item.mimeType.lowercase(Locale.getDefault()))
            append(' ')
            if (item.isVideo) append("video ") else append("foto fotoğraf image photo ")
            if (uploadedKeys.contains(item.mediaKey)) append("yedek bulut ")
        }
        return tokens.all { hay.contains(it) }
    }

    private fun buildLocalTimeline(media: List<LocalMedia>): List<TimelineItem> {
        val out = ArrayList<TimelineItem>()
        var lastKey = ""
        val sorted = media.sortedByDescending { it.dateTakenMs }
        for (item in sorted) {
            val key = when (timelineZoom) {
                TimelineZoom.DAY -> dayKey(item.dateTakenMs)
                TimelineZoom.MONTH -> monthKey(item.dateTakenMs)
                TimelineZoom.YEAR -> yearKey(item.dateTakenMs)
            }
            if (key != lastKey) {
                val label = when (timelineZoom) {
                    TimelineZoom.DAY -> formatDayLabel(item.dateTakenMs)
                    TimelineZoom.MONTH -> formatMonthLabel(item.dateTakenMs)
                    TimelineZoom.YEAR -> formatYearLabel(item.dateTakenMs)
                }
                out += TimelineItem.Header(label, key)
                lastKey = key
            }
            out += TimelineItem.Photo(item)
        }
        return out
    }

    private fun buildCloudTimeline(files: List<FileEntry>): List<TimelineItem> {
        val out = ArrayList<TimelineItem>()
        if (files.isEmpty()) return out
        var lastKey = ""
        val sorted = files.sortedByDescending { it.sortTimeMs.coerceAtLeast(0L) }
        for (f in sorted) {
            val ms = f.sortTimeMs.coerceAtLeast(0L)
            val key = when {
                ms <= 0L -> "unknown"
                timelineZoom == TimelineZoom.DAY -> dayKey(ms)
                timelineZoom == TimelineZoom.MONTH -> monthKey(ms)
                else -> yearKey(ms)
            }
            if (key != lastKey) {
                val label = when {
                    ms <= 0L -> "Tarihsiz yedekler"
                    timelineZoom == TimelineZoom.DAY -> formatDayLabel(ms)
                    timelineZoom == TimelineZoom.MONTH -> formatMonthLabel(ms)
                    else -> formatYearLabel(ms)
                }
                out += TimelineItem.Header(label, key)
                lastKey = key
            }
            out += TimelineItem.Cloud(f)
        }
        return out
    }

    private fun dayKey(ms: Long): String {
        val c = Calendar.getInstance().apply { timeInMillis = ms.coerceAtLeast(0L) }
        return "%04d-%02d-%02d".format(c.get(Calendar.YEAR), c.get(Calendar.MONTH) + 1, c.get(Calendar.DAY_OF_MONTH))
    }

    private fun monthKey(ms: Long): String {
        val c = Calendar.getInstance().apply { timeInMillis = ms.coerceAtLeast(0L) }
        return "%04d-%02d".format(c.get(Calendar.YEAR), c.get(Calendar.MONTH) + 1)
    }

    private fun yearKey(ms: Long): String {
        val c = Calendar.getInstance().apply { timeInMillis = ms.coerceAtLeast(0L) }
        return "%04d".format(c.get(Calendar.YEAR))
    }

    private fun formatDayLabel(ms: Long): String {
        val cal = Calendar.getInstance()
        val today = dayKey(cal.timeInMillis)
        cal.add(Calendar.DAY_OF_YEAR, -1)
        val yesterday = dayKey(cal.timeInMillis)
        val key = dayKey(ms)
        return when (key) {
            today -> "Bugün"
            yesterday -> "Dün"
            else -> SimpleDateFormat("d MMMM yyyy", Locale("tr", "TR")).format(Date(ms))
        }
    }

    private fun formatMonthLabel(ms: Long): String =
        SimpleDateFormat("MMMM yyyy", Locale("tr", "TR")).format(Date(ms))

    private fun formatYearLabel(ms: Long): String =
        SimpleDateFormat("yyyy", Locale("tr", "TR")).format(Date(ms))

    private fun enterSelection() {
        selectionMode = true
        btnSelect.text = "İptal"
        selectionBarScroll.visibility = View.VISIBLE
        galleryBottomNav.visibility = View.GONE
        timelineAdapter.notifyDataSetChanged()
        refreshSelectionUi()
    }

    private fun exitSelection(refresh: Boolean = true) {
        selectionMode = false
        selectedLocal.clear()
        selectedCloud.clear()
        btnSelect.text = "Seç"
        selectionBarScroll.visibility = View.GONE
        galleryBottomNav.visibility = View.VISIBLE
        if (refresh) timelineAdapter.notifyDataSetChanged()
    }

    private fun toggleLocal(item: LocalMedia) {
        if (!selectedLocal.add(item.mediaKey)) selectedLocal.remove(item.mediaKey)
        if (selectedLocal.isEmpty() && selectedCloud.isEmpty()) {
            exitSelection()
            return
        }
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
    }

    private fun toggleCloud(item: FileEntry) {
        if (item.kind == "folder") return
        if (!selectedCloud.add(item.id)) selectedCloud.remove(item.id)
        if (selectedLocal.isEmpty() && selectedCloud.isEmpty()) {
            exitSelection()
            return
        }
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
    }

    private fun refreshSelectionUi() {
        val n = selectedLocal.size + selectedCloud.size
        selectionCount.text = "$n seçili"
        findViewById<Button>(R.id.btnShareSelected).isEnabled = n > 0
        findViewById<Button>(R.id.btnUploadSelected).isEnabled = selectedLocal.isNotEmpty()
        findViewById<Button>(R.id.btnDeleteSelected).isEnabled = n > 0
        findViewById<Button>(R.id.btnOpenSelected).isEnabled = n == 1
        findViewById<Button>(R.id.btnInfoSelected).isEnabled = n == 1
    }

    private fun selectAllVisible() {
        when (tab) {
            Tab.PHOTOS, Tab.ALBUMS -> {
                val media = if (searchQuery.isBlank()) allLocal else allLocal.filter {
                    it.displayName.contains(searchQuery, true)
                }
                selectedLocal.clear()
                selectedLocal.addAll(media.map { it.mediaKey })
            }
            Tab.CLOUD -> {
                selectedCloud.clear()
                selectedCloud.addAll(allCloud.filter { it.kind == "file" }.map { it.id })
            }
        }
        if (!selectionMode) enterSelection()
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
    }

    private fun shareSelected() {
        val localItems = allLocal.filter { it.mediaKey in selectedLocal }
        if (localItems.isEmpty() && selectedCloud.isNotEmpty()) {
            shareCloudSelected()
            return
        }
        if (localItems.isEmpty()) {
            Toast.makeText(this, "Seçili yerel öğe yok", Toast.LENGTH_SHORT).show()
            return
        }
        GalleryShareHelper.showLocalShareSheet(this, localItems)
    }

    private fun shareCloudSelected() {
        val entry = allCloud.firstOrNull { it.id in selectedCloud && it.kind == "file" }
        if (entry == null) {
            Toast.makeText(this, "Paylaşılacak bulut dosyası yok", Toast.LENGTH_SHORT).show()
            return
        }
        AlertDialog.Builder(this)
            .setTitle("Bulut paylaşımı")
            .setItems(arrayOf("İndirip uygulamalarla paylaş", "Bağlantı oluştur", "Bağlantıyı kopyala")) { _, which ->
                when (which) {
                    0 -> shareCloudAfterDownload(entry)
                    1, 2 -> createCloudShareLink(entry, copyOnly = which == 2)
                }
            }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun shareCloudAfterDownload(entry: FileEntry) {
        progress.visibility = View.VISIBLE
        lifecycleScope.launch {
            try {
                val file = withContext(Dispatchers.IO) { api.downloadToCache(entry) }
                GalleryShareHelper.shareFile(this@PhotosLibraryActivity, file, entry.name, MainActivity.resolveMime(entry))
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun createCloudShareLink(entry: FileEntry, copyOnly: Boolean) {
        progress.visibility = View.VISIBLE
        lifecycleScope.launch {
            try {
                val resp = withContext(Dispatchers.IO) { api.createShareLink(entry.id) }
                val url = resp.url.ifBlank { throw IllegalStateException("Boş bağlantı") }
                if (copyOnly) {
                    GalleryShareHelper.copyText(this@PhotosLibraryActivity, url, "Bağlantı kopyalandı")
                } else {
                    GalleryShareHelper.shareTextLink(this@PhotosLibraryActivity, url, entry.name)
                }
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message ?: "Bağlantı oluşturulamadı", Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun shareLocalItems(items: List<LocalMedia>) {
        GalleryShareHelper.showLocalShareSheet(this, items)
    }

    private fun uploadSelectedToCloud() {
        if (!session.isLoggedIn) {
            Toast.makeText(this, "Buluta yüklemek için giriş yapın", Toast.LENGTH_LONG).show()
            return
        }
        val items = allLocal.filter { it.mediaKey in selectedLocal }
        if (items.isEmpty()) {
            Toast.makeText(this, "Yüklenecek yerel öğe seçin", Toast.LENGTH_SHORT).show()
            return
        }
        progress.visibility = View.VISIBLE
        lifecycleScope.launch {
            try {
                withContext(Dispatchers.IO) {
                    val db = UploadQueueDb(applicationContext)
                    for (item in items) {
                        val parent = api.ensurePhotosAlbumFolder(item)
                        db.enqueue(
                            source = "gallery:${item.mediaKey}",
                            parentId = parent,
                            localUri = item.uri.toString(),
                            displayName = item.displayName,
                            conflictPolicy = UploadConflictPolicy.ASK,
                        )
                    }
                }
                UploadForegroundService.startDrain(this@PhotosLibraryActivity)
                Toast.makeText(
                    this@PhotosLibraryActivity,
                    "${items.size} dosya arka planda yükleniyor",
                    Toast.LENGTH_LONG,
                ).show()
                exitSelection()
            } catch (e: Exception) {
                Toast.makeText(
                    this@PhotosLibraryActivity,
                    "Yükleme başlatılamadı: ${e.message}",
                    Toast.LENGTH_LONG,
                ).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun openSelectedExternal() {
        val local = allLocal.firstOrNull { it.mediaKey in selectedLocal }
        if (local != null) {
            openLocalExternal(local)
            return
        }
        val cloud = allCloud.firstOrNull { it.id in selectedCloud && it.kind == "file" }
        if (cloud != null) {
            openCloud(cloud)
            return
        }
        Toast.makeText(this, "Seçili öğe yok", Toast.LENGTH_SHORT).show()
    }

    private fun openLocalExternal(item: LocalMedia) {
        try {
            startActivity(
                Intent.createChooser(
                    Intent(Intent.ACTION_VIEW).apply {
                        setDataAndType(item.uri, item.mimeType.ifBlank { "*/*" })
                        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    },
                    "Birlikte aç",
                ),
            )
        } catch (e: Exception) {
            Toast.makeText(this, e.message, Toast.LENGTH_LONG).show()
        }
    }

    private fun showSelectedInfo() {
        val local = allLocal.firstOrNull { it.mediaKey in selectedLocal }
        if (local != null) {
            showLocalInfo(local)
            return
        }
        val cloud = allCloud.firstOrNull { it.id in selectedCloud }
        if (cloud != null) {
            showCloudInfo(cloud)
            return
        }
        Toast.makeText(this, "Seçili öğe yok", Toast.LENGTH_SHORT).show()
    }

    private fun showLocalInfo(item: LocalMedia) {
        val date = if (item.dateTakenMs > 0) {
            SimpleDateFormat("d MMMM yyyy HH:mm", Locale("tr", "TR")).format(Date(item.dateTakenMs))
        } else {
            "—"
        }
        val size = if (item.sizeBytes > 0) Formatter.formatFileSize(this, item.sizeBytes) else "—"
        AlertDialog.Builder(this)
            .setTitle(item.displayName)
            .setMessage(
                "Tür: ${item.mimeType}\n" +
                    "Boyut: $size\n" +
                    "Tarih: $date\n" +
                    "Albüm: ${item.albumName ?: "—"}\n" +
                    (if (uploadedKeys.contains(item.mediaKey)) "Yedek: bulutta ☁\n" else "Yedek: henüz yüklenmedi\n") +
                    (if (item.isVideo) "Video" else "Fotoğraf"),
            )
            .setPositiveButton("Tamam", null)
            .setNeutralButton("Paylaş") { _, _ -> shareLocalItems(listOf(item)) }
            .show()
    }

    private fun showCloudInfo(entry: FileEntry) {
        val size = if (entry.sizeBytes > 0) Formatter.formatFileSize(this, entry.sizeBytes) else "—"
        AlertDialog.Builder(this)
            .setTitle(entry.name)
            .setMessage(
                "Tür: ${MainActivity.resolveMime(entry)}\n" +
                    "Boyut: $size\n" +
                    "Kimlik: ${entry.id.take(12)}…\n" +
                    if (entry.starred) "Yıldızlı" else "Bulut dosyası",
            )
            .setPositiveButton("Tamam", null)
            .show()
    }

    private fun showLocalContextMenu(media: LocalMedia, anchor: View) {
        if (selectionMode) {
            toggleLocal(media)
            return
        }
        PopupMenu(this, anchor).apply {
            menu.add(0, 1, 0, "Paylaş")
            menu.add(0, 2, 1, "Buluta yükle")
            menu.add(0, 3, 2, "Birlikte aç")
            menu.add(0, 4, 3, "Ayrıntılar")
            menu.add(0, 5, 4, "Seç")
            menu.add(0, 6, 5, "Sil")
            setOnMenuItemClickListener { item ->
                when (item.itemId) {
                    1 -> shareLocalItems(listOf(media))
                    2 -> {
                        selectedLocal.clear()
                        selectedLocal.add(media.mediaKey)
                        enterSelection()
                        uploadSelectedToCloud()
                    }
                    3 -> openLocalExternal(media)
                    4 -> showLocalInfo(media)
                    5 -> {
                        enterSelection()
                        toggleLocal(media)
                    }
                    6 -> {
                        selectedLocal.clear()
                        selectedLocal.add(media.mediaKey)
                        confirmDeleteSelected()
                    }
                }
                true
            }
            show()
        }
    }

    private fun showCloudContextMenu(entry: FileEntry, anchor: View) {
        if (selectionMode) {
            toggleCloud(entry)
            return
        }
        if (entry.kind == "folder") {
            openCloud(entry)
            return
        }
        PopupMenu(this, anchor).apply {
            menu.add(0, 1, 0, "Önizle")
            menu.add(0, 2, 1, "Ayrıntılar")
            menu.add(0, 3, 2, if (entry.starred) "Yıldızı kaldır" else "Yıldızla")
            menu.add(0, 4, 3, "Seç")
            menu.add(0, 5, 4, "Sil")
            setOnMenuItemClickListener { item ->
                when (item.itemId) {
                    1 -> openCloud(entry)
                    2 -> showCloudInfo(entry)
                    3 -> lifecycleScope.launch {
                        runCatching {
                            withContext(Dispatchers.IO) { api.setStarred(entry.id, !entry.starred) }
                        }.onSuccess {
                            Toast.makeText(this@PhotosLibraryActivity, "Güncellendi", Toast.LENGTH_SHORT).show()
                            reload()
                        }.onFailure {
                            Toast.makeText(this@PhotosLibraryActivity, it.message, Toast.LENGTH_LONG).show()
                        }
                    }
                    4 -> {
                        enterSelection()
                        toggleCloud(entry)
                    }
                    5 -> {
                        selectedCloud.clear()
                        selectedCloud.add(entry.id)
                        confirmDeleteSelected()
                    }
                }
                true
            }
            show()
        }
    }

    private fun confirmDeleteSelected() {
        val localItems = allLocal.filter { it.mediaKey in selectedLocal }
        val cloudIds = selectedCloud.toList()
        if (localItems.isEmpty() && cloudIds.isEmpty()) {
            Toast.makeText(this, "Seçili öğe yok", Toast.LENGTH_SHORT).show()
            return
        }
        val notBackedUp = localItems.count { it.mediaKey !in uploadedKeys }
        val msg = buildString {
            if (localItems.isNotEmpty()) append("${localItems.size} yerel dosya silinecek")
            if (notBackedUp > 0) {
                append("\n⚠ $notBackedUp dosya henüz yedeklenmemiş — bulutta kopyası olmayabilir")
            }
            if (cloudIds.isNotEmpty()) {
                if (isNotEmpty()) append("\n")
                append("${cloudIds.size} bulut dosyası sunucudan silinecek")
            }
            append("\n\nBu işlem geri alınamaz.")
            append("\n\nNot: “Yer aç” yalnızca yedeklenenleri siler; bu Sil düğmesi seçiminizi siler.")
        }
        AlertDialog.Builder(this)
            .setTitle("Silinsin mi?")
            .setMessage(msg)
            .setPositiveButton("Sil") { _, _ -> deleteSelected(localItems, cloudIds) }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun deleteSelected(localItems: List<LocalMedia>, cloudIds: List<String>) {
        lifecycleScope.launch {
            if (cloudIds.isNotEmpty()) {
                progress.visibility = View.VISIBLE
                var ok = 0
                for (id in cloudIds) {
                    runCatching { withContext(Dispatchers.IO) { api.delete(id) } }.onSuccess { ok++ }
                }
                Toast.makeText(this@PhotosLibraryActivity, "$ok bulut dosyası silindi", Toast.LENGTH_SHORT).show()
                progress.visibility = View.GONE
            }
            if (localItems.isNotEmpty()) {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    pendingLocalDelete = localItems
                    val request = MediaStore.createDeleteRequest(contentResolver, localItems.map { it.uri })
                    deleteRequestLauncher.launch(IntentSenderRequest.Builder(request.intentSender).build())
                    return@launch
                }
                var deleted = 0
                for (item in localItems) {
                    val rows = runCatching { contentResolver.delete(item.uri, null, null) }.getOrDefault(0)
                    if (rows > 0) deleted++
                }
                Toast.makeText(this@PhotosLibraryActivity, "$deleted yerel dosya silindi", Toast.LENGTH_SHORT).show()
            }
            exitSelection()
            reload()
        }
    }

    private fun openLocal(item: LocalMedia) {
        if (pickMode) {
            val result = Intent().apply {
                data = item.uri
                flags = Intent.FLAG_GRANT_READ_URI_PERMISSION
                clipData = android.content.ClipData.newUri(contentResolver, item.displayName, item.uri)
            }
            setResult(Activity.RESULT_OK, result)
            finish()
            return
        }
        startActivity(
            Intent(this, MediaPreviewActivity::class.java).apply {
                putExtra(MediaPreviewActivity.EXTRA_NAME, item.displayName)
                putExtra(MediaPreviewActivity.EXTRA_MIME, item.mimeType)
                putExtra(MediaPreviewActivity.EXTRA_LOCAL_URI, item.uri.toString())
            },
        )
    }

    private fun openCloud(entry: FileEntry) {
        if (entry.kind == "folder") {
            viewingCloudAlbum = entry
            loadCloudAlbumContents(entry)
            return
        }
        startActivity(
            Intent(this, MediaPreviewActivity::class.java).apply {
                putExtra(MediaPreviewActivity.EXTRA_ID, entry.id)
                putExtra(MediaPreviewActivity.EXTRA_NAME, entry.name)
                putExtra(MediaPreviewActivity.EXTRA_MIME, MainActivity.resolveMime(entry))
            },
        )
    }

    private fun mediaPermissions(): Array<String> = MediaAccess.mediaPermissionsForRequest()

    private fun hasMediaPermission(): Boolean = MediaAccess.hasMediaAccess(this)

    // --- adapters ---

    private class TimelineAdapter(
        private val onOpenLocal: (LocalMedia) -> Unit,
        private val onLongLocal: (LocalMedia, View) -> Unit,
        private val onOpenCloud: (FileEntry) -> Unit,
        private val onLongCloud: (FileEntry, View) -> Unit,
        private val isLocalSelected: (LocalMedia) -> Boolean,
        private val isCloudSelected: (FileEntry) -> Boolean,
        private val selectionActive: () -> Boolean,
        private val isBackedUp: (LocalMedia) -> Boolean,
    ) : RecyclerView.Adapter<RecyclerView.ViewHolder>() {
        private var items: List<TimelineItem> = emptyList()
        private var token = ""
        private var serverUrl = ""

        fun updateAuth(token: String, serverUrl: String) {
            this.token = token
            this.serverUrl = serverUrl.trimEnd('/')
        }

        fun headerLabelAtOrBefore(position: Int): String? {
            for (i in position downTo 0) {
                val item = items.getOrNull(i) ?: continue
                if (item is TimelineItem.Header) return item.label
            }
            return null
        }

        fun submitLocal(next: List<TimelineItem>) {
            val diff = DiffUtil.calculateDiff(TimelineDiff(items, next))
            items = next
            diff.dispatchUpdatesTo(this)
        }

        fun submitCloud(next: List<TimelineItem>) {
            val diff = DiffUtil.calculateDiff(TimelineDiff(items, next))
            items = next
            diff.dispatchUpdatesTo(this)
        }

        private class TimelineDiff(
            private val old: List<TimelineItem>,
            private val new: List<TimelineItem>,
        ) : DiffUtil.Callback() {
            override fun getOldListSize() = old.size
            override fun getNewListSize() = new.size
            override fun areItemsTheSame(o: Int, n: Int): Boolean {
                val a = old[o]
                val b = new[n]
                return when {
                    a is TimelineItem.Header && b is TimelineItem.Header -> a.dayKey == b.dayKey
                    a is TimelineItem.Photo && b is TimelineItem.Photo -> a.media.mediaKey == b.media.mediaKey
                    a is TimelineItem.Cloud && b is TimelineItem.Cloud -> a.entry.id == b.entry.id
                    else -> false
                }
            }
            override fun areContentsTheSame(o: Int, n: Int): Boolean = old[o] == new[n]
        }

        fun isHeader(position: Int): Boolean = items.getOrNull(position) is TimelineItem.Header

        override fun getItemViewType(position: Int): Int = when (items[position]) {
            is TimelineItem.Header -> 0
            is TimelineItem.Photo, is TimelineItem.Cloud -> 1
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): RecyclerView.ViewHolder {
            return if (viewType == 0) {
                val v = LayoutInflater.from(parent.context).inflate(R.layout.item_photo_section, parent, false)
                HeaderVH(v)
            } else {
                val v = LayoutInflater.from(parent.context).inflate(R.layout.item_photo_cell, parent, false)
                CellVH(v)
            }
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(holder: RecyclerView.ViewHolder, position: Int) {
            when (val item = items[position]) {
                is TimelineItem.Header -> (holder as HeaderVH).title.text = item.label
                is TimelineItem.Photo -> bindLocal(holder as CellVH, item.media)
                is TimelineItem.Cloud -> bindCloud(holder as CellVH, item.entry)
            }
        }

        override fun onViewRecycled(holder: RecyclerView.ViewHolder) {
            if (holder is CellVH) {
                MediaThumbLoader.cancelLoad(holder.thumb)
            }
        }

        private fun bindLocal(holder: CellVH, media: LocalMedia) {
            holder.badge.visibility = if (media.isVideo) View.VISIBLE else View.GONE
            if (media.isVideo) holder.badge.text = "▶"
            holder.nameHint.visibility = View.GONE
            holder.backupBadge.visibility = if (isBackedUp(media)) View.VISIBLE else View.GONE
            holder.check.visibility = if (selectionActive()) View.VISIBLE else View.GONE
            holder.check.setOnCheckedChangeListener(null)
            holder.check.isChecked = isLocalSelected(media)
            holder.thumb.clearColorFilter()
            holder.thumb.setBackgroundColor(Color.parseColor("#DCE6F5"))
            MediaThumbLoader.loadLocal(holder.thumb, media.uri, media.mediaKey)
            holder.itemView.setOnClickListener { onOpenLocal(media) }
            holder.itemView.setOnLongClickListener {
                onLongLocal(media, holder.itemView)
                true
            }
        }

        private fun bindCloud(holder: CellVH, entry: FileEntry) {
            holder.badge.visibility = View.GONE
            holder.nameHint.visibility = View.GONE
            holder.backupBadge.visibility = View.VISIBLE
            holder.backupBadge.text = "☁"
            holder.check.visibility = if (selectionActive() && entry.kind == "file") View.VISIBLE else View.GONE
            holder.check.setOnCheckedChangeListener(null)
            holder.check.isChecked = isCloudSelected(entry)
            holder.thumb.clearColorFilter()
            if (entry.kind == "folder") {
                holder.thumb.setImageDrawable(null)
                holder.thumb.tag = null
                holder.thumb.setBackgroundColor(Color.parseColor("#0B5CAD"))
                holder.badge.visibility = View.VISIBLE
                holder.badge.text = "Albüm"
                holder.backupBadge.visibility = View.GONE
            } else {
                holder.thumb.setBackgroundColor(Color.parseColor("#DCE6F5"))
                val mime = MainActivity.resolveMime(entry)
                val isVideo = mime.startsWith("video/")
                if (isVideo) {
                    holder.badge.visibility = View.VISIBLE
                    holder.badge.text = "▶"
                }
                if (token.isNotBlank() && serverUrl.isNotBlank() &&
                    (mime.startsWith("image/") || isVideo)
                ) {
                    val url = "$serverUrl/api/files/download/${entry.id}?inline=1"
                    MediaThumbLoader.loadRemote(
                        holder.thumb,
                        url,
                        token,
                        "cloud:${entry.id}",
                        isVideo = isVideo,
                    )
                } else {
                    holder.thumb.tag = null
                    holder.thumb.setImageDrawable(null)
                }
            }
            holder.itemView.setOnClickListener { onOpenCloud(entry) }
            holder.itemView.setOnLongClickListener {
                onLongCloud(entry, holder.itemView)
                true
            }
        }

        class HeaderVH(view: View) : RecyclerView.ViewHolder(view) {
            val title: TextView = view.findViewById(R.id.sectionTitle)
        }

        class CellVH(view: View) : RecyclerView.ViewHolder(view) {
            val thumb: ImageView = view.findViewById(R.id.photoThumb)
            val badge: TextView = view.findViewById(R.id.photoBadge)
            val nameHint: TextView = view.findViewById(R.id.photoNameHint)
            val backupBadge: TextView = view.findViewById(R.id.photoBackupBadge)
            val check: CheckBox = view.findViewById(R.id.photoCheck)
        }
    }

    private class AlbumGridAdapter(
        private val onOpen: (MediaAlbum) -> Unit,
    ) : RecyclerView.Adapter<AlbumGridAdapter.VH>() {
        private var items: List<MediaAlbum> = emptyList()
        private var authToken: String = ""
        private var serverUrl: String = ""

        fun updateAuth(token: String, server: String) {
            authToken = token
            serverUrl = server.trimEnd('/')
        }

        fun submit(next: List<MediaAlbum>) {
            val old = items
            items = next
            DiffUtil.calculateDiff(object : DiffUtil.Callback() {
                override fun getOldListSize() = old.size
                override fun getNewListSize() = next.size
                override fun areItemsTheSame(o: Int, n: Int) = old[o].id == next[n].id
                override fun areContentsTheSame(o: Int, n: Int) =
                    old[o].name == next[n].name &&
                        old[o].count == next[n].count &&
                        old[o].coverUri == next[n].coverUri &&
                        old[o].coverRemoteId == next[n].coverRemoteId
            }).dispatchUpdatesTo(this)
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
            val v = LayoutInflater.from(parent.context).inflate(R.layout.item_album_tile, parent, false)
            return VH(v)
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(holder: VH, position: Int) {
            val album = items[position]
            holder.name.text = album.name
            holder.count.text = if (album.count > 0) "${album.count} öğe" else "Albüm"
            holder.cover.setBackgroundColor(Color.parseColor("#DCE6F5"))
            val cover = album.coverUri
            val remoteId = album.coverRemoteId
            when {
                cover != null -> MediaThumbLoader.loadLocal(holder.cover, cover, "album:${album.id}")
                !remoteId.isNullOrBlank() && serverUrl.isNotBlank() -> {
                    val url = "$serverUrl/api/files/download/$remoteId?inline=1"
                    MediaThumbLoader.loadRemote(
                        holder.cover,
                        url,
                        authToken,
                        "album-cloud:$remoteId",
                        isVideo = false,
                    )
                }
                else -> {
                    MediaThumbLoader.cancelLoad(holder.cover)
                    holder.cover.setImageDrawable(null)
                }
            }
            holder.itemView.setOnClickListener { onOpen(album) }
        }

        override fun onViewRecycled(holder: VH) {
            MediaThumbLoader.cancelLoad(holder.cover)
        }

        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val cover: ImageView = view.findViewById(R.id.albumCover)
            val name: TextView = view.findViewById(R.id.albumName)
            val count: TextView = view.findViewById(R.id.albumCount)
        }
    }
}
