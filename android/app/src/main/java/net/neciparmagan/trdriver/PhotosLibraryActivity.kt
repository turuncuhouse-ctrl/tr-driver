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
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.IntentSenderRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.GridLayoutManager
import androidx.recyclerview.widget.RecyclerView
import coil.load
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.LocalMedia
import net.neciparmagan.trdriver.data.MediaAlbum
import net.neciparmagan.trdriver.data.MediaCatalog
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.Headers
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
    private lateinit var selectionCount: TextView
    private lateinit var tabPhotos: Button
    private lateinit var tabAlbums: Button
    private lateinit var tabCloud: Button
    private lateinit var btnSelect: Button

    private var tab = Tab.PHOTOS
    private var viewingAlbum: MediaAlbum? = null
    private var viewingCloudAlbum: FileEntry? = null
    private var showingCloudAlbumList = false
    private var selectionMode = false
    private var searchQuery = ""
    private var searchJob: Job? = null

    private var allLocal: List<LocalMedia> = emptyList()
    private var allAlbums: List<MediaAlbum> = emptyList()
    private var allCloud: List<FileEntry> = emptyList()

    private val selectedLocal = linkedSetOf<String>() // mediaKey
    private val selectedCloud = linkedSetOf<String>() // file id
    private var pendingLocalDelete: List<LocalMedia> = emptyList()

    private val timelineAdapter = TimelineAdapter(
        onOpenLocal = { if (selectionMode) toggleLocal(it) else openLocal(it) },
        onLongLocal = { enterSelection(); toggleLocal(it) },
        onOpenCloud = { if (selectionMode) toggleCloud(it) else openCloud(it) },
        onLongCloud = { enterSelection(); toggleCloud(it) },
        isLocalSelected = { selectedLocal.contains(it.mediaKey) },
        isCloudSelected = { selectedCloud.contains(it.id) },
        selectionActive = { selectionMode },
    )
    private val albumAdapter = AlbumGridAdapter { openAlbum(it) }

    private val permissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { result ->
            if (result.values.any { it }) reload()
            else Toast.makeText(this, "Galeri izni gerekli", Toast.LENGTH_LONG).show()
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
        selectionCount = findViewById(R.id.selectionCount)
        tabPhotos = findViewById(R.id.tabPhotos)
        tabAlbums = findViewById(R.id.tabAlbums)
        tabCloud = findViewById(R.id.tabCloud)
        btnSelect = findViewById(R.id.btnPhotosSelect)

        val glm = GridLayoutManager(this, 3)
        glm.spanSizeLookup = object : GridLayoutManager.SpanSizeLookup() {
            override fun getSpanSize(position: Int): Int {
                return if (grid.adapter === timelineAdapter && timelineAdapter.isHeader(position)) 3 else 1
            }
        }
        grid.layoutManager = glm
        timelineAdapter.updateAuth(session.token.orEmpty(), session.serverUrl)

        tabPhotos.setOnClickListener { selectTab(Tab.PHOTOS) }
        tabAlbums.setOnClickListener { selectTab(Tab.ALBUMS) }
        tabCloud.setOnClickListener { selectTab(Tab.CLOUD) }
        btnSelect.setOnClickListener {
            if (selectionMode) exitSelection() else enterSelection()
        }
        findViewById<Button>(R.id.btnPhotosFreeUp).setOnClickListener {
            startActivity(Intent(this, FreeUpSpaceActivity::class.java))
        }
        findViewById<Button>(R.id.btnPhotosClose).setOnClickListener { finish() }
        findViewById<Button>(R.id.btnSelectAll).setOnClickListener { selectAllVisible() }
        findViewById<Button>(R.id.btnShareSelected).setOnClickListener { shareSelected() }
        findViewById<Button>(R.id.btnDeleteSelected).setOnClickListener { confirmDeleteSelected() }
        findViewById<Button>(R.id.btnCancelSelect).setOnClickListener { exitSelection() }

        search.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit
            override fun afterTextChanged(s: Editable?) {
                searchJob?.cancel()
                searchJob = lifecycleScope.launch {
                    delay(250)
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

        if (!hasMediaPermission()) permissionLauncher.launch(mediaPermissions())
        selectTab(Tab.PHOTOS)
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
        }
        style(tabPhotos, tab == Tab.PHOTOS)
        style(tabAlbums, tab == Tab.ALBUMS)
        style(tabCloud, tab == Tab.CLOUD)
    }

    private fun reload() {
        when (tab) {
            Tab.PHOTOS -> loadAllPhotos()
            Tab.ALBUMS -> loadAlbums()
            Tab.CLOUD -> loadCloudTimeline()
        }
    }

    private fun loadAllPhotos() {
        title.text = "TR Photos"
        subtitle.text = "Tüm fotoğraf ve videolar · tarihe göre"
        if (!hasMediaPermission()) {
            subtitle.text = "Galeri izni gerekli"
            return
        }
        progress.visibility = View.VISIBLE
        grid.adapter = timelineAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            allLocal = withContext(Dispatchers.IO) { MediaCatalog.scan(this@PhotosLibraryActivity, 3000) }
            applyFilter()
            progress.visibility = View.GONE
        }
    }

    private fun loadAlbums() {
        title.text = "Albümler"
        subtitle.text = "Kamera, Ekran görüntüleri ve diğer klasörler"
        if (!hasMediaPermission()) {
            subtitle.text = "Galeri izni gerekli"
            return
        }
        progress.visibility = View.VISIBLE
        grid.adapter = albumAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 2
        lifecycleScope.launch {
            allAlbums = withContext(Dispatchers.IO) { MediaCatalog.listAlbums(this@PhotosLibraryActivity) }
            applyFilter()
            progress.visibility = View.GONE
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
        grid.adapter = timelineAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            allLocal = withContext(Dispatchers.IO) {
                MediaCatalog.scanAlbum(this@PhotosLibraryActivity, album.id)
            }
            applyFilter()
            progress.visibility = View.GONE
        }
    }

    private fun loadCloudTimeline() {
        if (!session.isLoggedIn) {
            Toast.makeText(this, "Bulut için giriş gerekli", Toast.LENGTH_LONG).show()
            return
        }
        viewingCloudAlbum = null
        showingCloudAlbumList = false
        title.text = "Bulut"
        subtitle.text = "TR Photos yedekleri · tarihe göre · albümler için alt yazıya dokunun"
        subtitle.setOnClickListener { loadCloudAlbums() }
        progress.visibility = View.VISIBLE
        grid.adapter = timelineAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            try {
                allCloud = withContext(Dispatchers.IO) { api.listCloudPhotosFlat(1200) }
                applyFilter()
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
        title.text = "Bulut albümleri"
        subtitle.text = "Cihaz klasörleri · zaman çizelgesi için Bulut sekmesi"
        subtitle.setOnClickListener(null)
        progress.visibility = View.VISIBLE
        grid.adapter = albumAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 2
        lifecycleScope.launch {
            try {
                val folders = withContext(Dispatchers.IO) { api.listCloudPhotoAlbums() }
                allAlbums = folders.map {
                    MediaAlbum(id = "cloud:${it.id}", name = it.name, count = 0, coverUri = null)
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
        title.text = folder.name
        subtitle.text = "Bulut albümü"
        progress.visibility = View.VISIBLE
        grid.adapter = timelineAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            try {
                val queue = ArrayDeque<String>()
                queue.add(folder.id)
                val out = ArrayList<FileEntry>()
                var guard = 0
                while (queue.isNotEmpty() && out.size < 1000 && guard < 400) {
                    guard++
                    val id = queue.removeFirst()
                    val children = withContext(Dispatchers.IO) { api.listFiles(id) }
                    for (c in children) {
                        if (c.kind == "folder") queue.add(c.id) else out += c
                    }
                }
                allCloud = out
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
                    subtitle.text = "${albums.size} bulut albümü" + if (q.isNotBlank()) " · \"$searchQuery\"" else ""
                } else {
                    grid.adapter = timelineAdapter
                    (grid.layoutManager as GridLayoutManager).spanCount = 3
                    val files = if (q.isBlank()) allCloud else allCloud.filter {
                        it.name.lowercase(Locale.getDefault()).contains(q)
                    }
                    timelineAdapter.submitCloud(buildCloudTimeline(files))
                    subtitle.text = when {
                        files.isEmpty() -> "Henüz bulutta medya yok · otomatik yedeği açın"
                        q.isNotBlank() -> "${files.size} bulut öğesi · \"$searchQuery\""
                        else -> "${files.size} bulut öğesi · albümler için alt yazıya dokunun"
                    }
                }
            }
        }
        refreshSelectionUi()
    }

    private fun showLocalTimeline(q: String) {
        grid.adapter = timelineAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        val media = if (q.isBlank()) allLocal else allLocal.filter {
            it.displayName.lowercase(Locale.getDefault()).contains(q) ||
                (it.albumName?.lowercase(Locale.getDefault())?.contains(q) == true)
        }
        timelineAdapter.submitLocal(buildLocalTimeline(media))
        subtitle.text = "${media.size} öğe · tarihe göre" +
            if (q.isNotBlank()) " · \"$searchQuery\"" else ""
    }

    private fun buildLocalTimeline(media: List<LocalMedia>): List<TimelineItem> {
        val out = ArrayList<TimelineItem>()
        var lastKey = ""
        for (item in media.sortedByDescending { it.dateTakenMs }) {
            val key = dayKey(item.dateTakenMs)
            if (key != lastKey) {
                out += TimelineItem.Header(formatDayLabel(item.dateTakenMs), key)
                lastKey = key
            }
            out += TimelineItem.Photo(item)
        }
        return out
    }

    private fun buildCloudTimeline(files: List<FileEntry>): List<TimelineItem> {
        // Cloud entries lack taken date in FileEntry — group by name heuristic or single section.
        val out = ArrayList<TimelineItem>()
        if (files.isEmpty()) return out
        out += TimelineItem.Header("Yedeklenenler", "cloud")
        for (f in files) out += TimelineItem.Cloud(f)
        return out
    }

    private fun dayKey(ms: Long): String {
        val c = Calendar.getInstance().apply { timeInMillis = ms.coerceAtLeast(0L) }
        return "%04d-%02d-%02d".format(c.get(Calendar.YEAR), c.get(Calendar.MONTH) + 1, c.get(Calendar.DAY_OF_MONTH))
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

    private fun enterSelection() {
        selectionMode = true
        btnSelect.text = "İptal"
        selectionBar.visibility = View.VISIBLE
        timelineAdapter.notifyDataSetChanged()
        refreshSelectionUi()
    }

    private fun exitSelection(refresh: Boolean = true) {
        selectionMode = false
        selectedLocal.clear()
        selectedCloud.clear()
        btnSelect.text = "Seç"
        selectionBar.visibility = View.GONE
        if (refresh) timelineAdapter.notifyDataSetChanged()
    }

    private fun toggleLocal(item: LocalMedia) {
        if (!selectedLocal.add(item.mediaKey)) selectedLocal.remove(item.mediaKey)
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
    }

    private fun toggleCloud(item: FileEntry) {
        if (item.kind == "folder") return
        if (!selectedCloud.add(item.id)) selectedCloud.remove(item.id)
        refreshSelectionUi()
        timelineAdapter.notifyDataSetChanged()
    }

    private fun refreshSelectionUi() {
        val n = selectedLocal.size + selectedCloud.size
        selectionCount.text = "$n seçili"
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
        val uris = ArrayList<Uri>()
        if (selectedLocal.isNotEmpty()) {
            for (m in allLocal) {
                if (m.mediaKey in selectedLocal) uris += m.uri
            }
        }
        if (uris.isEmpty() && selectedCloud.isNotEmpty()) {
            Toast.makeText(this, "Bulut öğeleri için önce indirin veya dosya tarayıcıdan paylaşın", Toast.LENGTH_LONG).show()
            return
        }
        if (uris.isEmpty()) {
            Toast.makeText(this, "Seçili öğe yok", Toast.LENGTH_SHORT).show()
            return
        }
        val send = Intent(Intent.ACTION_SEND_MULTIPLE).apply {
            type = "*/*"
            putParcelableArrayListExtra(Intent.EXTRA_STREAM, uris)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        startActivity(Intent.createChooser(send, "Paylaş"))
    }

    private fun confirmDeleteSelected() {
        val localItems = allLocal.filter { it.mediaKey in selectedLocal }
        val cloudIds = selectedCloud.toList()
        if (localItems.isEmpty() && cloudIds.isEmpty()) {
            Toast.makeText(this, "Seçili öğe yok", Toast.LENGTH_SHORT).show()
            return
        }
        val msg = buildString {
            if (localItems.isNotEmpty()) append("${localItems.size} yerel dosya silinecek")
            if (cloudIds.isNotEmpty()) {
                if (isNotEmpty()) append("\n")
                append("${cloudIds.size} bulut dosyası sunucudan silinecek")
            }
            append("\n\nBu işlem geri alınamaz.")
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

    private fun mediaPermissions(): Array<String> {
        return if (Build.VERSION.SDK_INT >= 33) {
            arrayOf(Manifest.permission.READ_MEDIA_IMAGES, Manifest.permission.READ_MEDIA_VIDEO)
        } else {
            arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
        }
    }

    private fun hasMediaPermission(): Boolean {
        return mediaPermissions().all {
            ContextCompat.checkSelfPermission(this, it) == PackageManager.PERMISSION_GRANTED
        }
    }

    // --- adapters ---

    private class TimelineAdapter(
        private val onOpenLocal: (LocalMedia) -> Unit,
        private val onLongLocal: (LocalMedia) -> Unit,
        private val onOpenCloud: (FileEntry) -> Unit,
        private val onLongCloud: (FileEntry) -> Unit,
        private val isLocalSelected: (LocalMedia) -> Boolean,
        private val isCloudSelected: (FileEntry) -> Boolean,
        private val selectionActive: () -> Boolean,
    ) : RecyclerView.Adapter<RecyclerView.ViewHolder>() {
        private var items: List<TimelineItem> = emptyList()
        private var token = ""
        private var serverUrl = ""

        fun updateAuth(token: String, serverUrl: String) {
            this.token = token
            this.serverUrl = serverUrl.trimEnd('/')
        }

        fun submitLocal(next: List<TimelineItem>) {
            items = next
            notifyDataSetChanged()
        }

        fun submitCloud(next: List<TimelineItem>) {
            items = next
            notifyDataSetChanged()
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

        private fun bindLocal(holder: CellVH, media: LocalMedia) {
            holder.badge.visibility = if (media.isVideo) View.VISIBLE else View.GONE
            if (media.isVideo) holder.badge.text = "▶"
            holder.check.visibility = if (selectionActive()) View.VISIBLE else View.GONE
            holder.check.setOnCheckedChangeListener(null)
            holder.check.isChecked = isLocalSelected(media)
            holder.thumb.load(media.uri) {
                size(280, 280)
                crossfade(true)
            }
            holder.itemView.setOnClickListener { onOpenLocal(media) }
            holder.itemView.setOnLongClickListener {
                onLongLocal(media)
                true
            }
        }

        private fun bindCloud(holder: CellVH, entry: FileEntry) {
            holder.thumb.setImageDrawable(null)
            holder.badge.visibility = View.GONE
            holder.check.visibility = if (selectionActive() && entry.kind == "file") View.VISIBLE else View.GONE
            holder.check.setOnCheckedChangeListener(null)
            holder.check.isChecked = isCloudSelected(entry)
            if (entry.kind == "folder") {
                holder.thumb.setBackgroundColor(Color.parseColor("#0B5CAD"))
                holder.badge.visibility = View.VISIBLE
                holder.badge.text = "Albüm"
            } else {
                holder.thumb.setBackgroundColor(Color.parseColor("#DCE6F5"))
                val mime = MainActivity.resolveMime(entry)
                if (mime.startsWith("video/")) {
                    holder.badge.visibility = View.VISIBLE
                    holder.badge.text = "▶"
                }
                if (token.isNotBlank() && serverUrl.isNotBlank() &&
                    (mime.startsWith("image/") || mime.startsWith("video/"))
                ) {
                    val url = "$serverUrl/api/files/download/${entry.id}?inline=1"
                    holder.thumb.load(url) {
                        headers(Headers.headersOf("Authorization", "Bearer $token"))
                        size(280, 280)
                        crossfade(true)
                    }
                }
            }
            holder.itemView.setOnClickListener { onOpenCloud(entry) }
            holder.itemView.setOnLongClickListener {
                onLongCloud(entry)
                true
            }
        }

        class HeaderVH(view: View) : RecyclerView.ViewHolder(view) {
            val title: TextView = view.findViewById(R.id.sectionTitle)
        }

        class CellVH(view: View) : RecyclerView.ViewHolder(view) {
            val thumb: ImageView = view.findViewById(R.id.photoThumb)
            val badge: TextView = view.findViewById(R.id.photoBadge)
            val check: CheckBox = view.findViewById(R.id.photoCheck)
        }
    }

    private class AlbumGridAdapter(
        private val onOpen: (MediaAlbum) -> Unit,
    ) : RecyclerView.Adapter<AlbumGridAdapter.VH>() {
        private var items: List<MediaAlbum> = emptyList()

        fun submit(next: List<MediaAlbum>) {
            items = next
            notifyDataSetChanged()
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
            holder.cover.setImageDrawable(null)
            holder.cover.setBackgroundColor(Color.parseColor("#0B5CAD"))
            album.coverUri?.let { uri ->
                holder.cover.load(uri) {
                    size(360, 360)
                    crossfade(true)
                }
            }
            holder.itemView.setOnClickListener { onOpen(album) }
        }

        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val cover: ImageView = view.findViewById(R.id.albumCover)
            val name: TextView = view.findViewById(R.id.albumName)
            val count: TextView = view.findViewById(R.id.albumCount)
        }
    }
}
