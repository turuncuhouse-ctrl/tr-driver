package net.neciparmagan.trdriver

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.ImageView
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.GridLayoutManager
import androidx.recyclerview.widget.RecyclerView
import coil.load
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.LocalMedia
import net.neciparmagan.trdriver.data.MediaAlbum
import net.neciparmagan.trdriver.data.MediaCatalog
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.Headers
import java.util.ArrayDeque

/**
 * Full gallery experience (Android Gallery / Google Photos style):
 * - Fotoğraflar: chronological grid of all device media
 * - Albümler: Camera, Screenshots, WhatsApp, … buckets
 * - Bulut: flat TR Photos timeline (no folder drilling)
 */
class PhotosLibraryActivity : AppCompatActivity() {
    private enum class Tab { PHOTOS, ALBUMS, CLOUD }

    private lateinit var session: SessionStore
    private lateinit var api: DriveApi
    private lateinit var title: TextView
    private lateinit var subtitle: TextView
    private lateinit var progress: ProgressBar
    private lateinit var grid: RecyclerView
    private lateinit var tabPhotos: Button
    private lateinit var tabAlbums: Button
    private lateinit var tabCloud: Button

    private var tab = Tab.PHOTOS
    private var viewingAlbum: MediaAlbum? = null
    private var viewingCloudAlbum: FileEntry? = null

    private val mediaAdapter = MediaGridAdapter { openLocal(it) }
    private val albumAdapter = AlbumGridAdapter { openAlbum(it) }
    private val cloudAdapter = CloudGridAdapter { openCloud(it) }

    private val permissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { result ->
            if (result.values.any { it }) {
                reload()
            } else {
                Toast.makeText(this, "Galeri izni gerekli", Toast.LENGTH_LONG).show()
            }
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
        tabPhotos = findViewById(R.id.tabPhotos)
        tabAlbums = findViewById(R.id.tabAlbums)
        tabCloud = findViewById(R.id.tabCloud)

        grid.layoutManager = GridLayoutManager(this, 3)
        cloudAdapter.updateAuth(session.token.orEmpty(), session.serverUrl)

        tabPhotos.setOnClickListener { selectTab(Tab.PHOTOS) }
        tabAlbums.setOnClickListener { selectTab(Tab.ALBUMS) }
        tabCloud.setOnClickListener { selectTab(Tab.CLOUD) }
        findViewById<Button>(R.id.btnPhotosFreeUp).setOnClickListener {
            startActivity(Intent(this, FreeUpSpaceActivity::class.java))
        }
        findViewById<Button>(R.id.btnPhotosClose).setOnClickListener { finish() }

        onBackPressedDispatcher.addCallback(this, object : androidx.activity.OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                when {
                    viewingAlbum != null -> {
                        viewingAlbum = null
                        selectTab(Tab.ALBUMS)
                    }
                    viewingCloudAlbum != null -> {
                        viewingCloudAlbum = null
                        loadCloudAlbums()
                    }
                    else -> {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                    }
                }
            }
        })

        if (!hasMediaPermission()) {
            permissionLauncher.launch(mediaPermissions())
        }
        selectTab(Tab.PHOTOS)
    }

    private fun selectTab(next: Tab) {
        tab = next
        viewingAlbum = null
        viewingCloudAlbum = null
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
            Tab.CLOUD -> {
                if (viewingCloudAlbum != null) loadCloudAlbumContents(viewingCloudAlbum!!)
                else loadCloudTimeline()
            }
        }
    }

    private fun loadAllPhotos() {
        title.text = "TR Photos"
        subtitle.text = "Tüm fotoğraf ve videolar · cihaz galerisi"
        if (!hasMediaPermission()) {
            subtitle.text = "Galeri izni gerekli"
            return
        }
        progress.visibility = View.VISIBLE
        grid.adapter = mediaAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            val items = withContext(Dispatchers.IO) { MediaCatalog.scan(this@PhotosLibraryActivity, 2500) }
            mediaAdapter.submit(items)
            subtitle.text = "${items.size} öğe · en yeniler üstte"
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
            val albums = withContext(Dispatchers.IO) { MediaCatalog.listAlbums(this@PhotosLibraryActivity) }
            albumAdapter.submit(albums)
            subtitle.text = "${albums.size} albüm"
            progress.visibility = View.GONE
        }
    }

    private fun openAlbum(album: MediaAlbum) {
        viewingAlbum = album
        title.text = album.name
        subtitle.text = "${album.count} öğe"
        progress.visibility = View.VISIBLE
        grid.adapter = mediaAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            val items = withContext(Dispatchers.IO) {
                MediaCatalog.scanAlbum(this@PhotosLibraryActivity, album.id)
            }
            mediaAdapter.submit(items)
            progress.visibility = View.GONE
        }
    }

    private fun loadCloudTimeline() {
        if (!session.isLoggedIn) {
            Toast.makeText(this, "Bulut için giriş gerekli", Toast.LENGTH_LONG).show()
            return
        }
        title.text = "Bulut"
        subtitle.text = "TR Photos · yedeklenenler (klasörsüz zaman çizelgesi)"
        progress.visibility = View.VISIBLE
        grid.adapter = cloudAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            try {
                val files = withContext(Dispatchers.IO) { api.listCloudPhotosFlat(1000) }
                cloudAdapter.submit(files)
                subtitle.text = if (files.isEmpty()) {
                    "Henüz bulutta medya yok · otomatik yedeği açın"
                } else {
                    "${files.size} bulut öğesi · dokunarak aç"
                }
                // Also offer album chips via long-press subtitle? Secondary: load albums button in subtitle click
                subtitle.setOnClickListener { loadCloudAlbums() }
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun loadCloudAlbums() {
        viewingCloudAlbum = null
        title.text = "Bulut albümleri"
        subtitle.text = "Cihaz klasörleri (TR Photos) · geri: zaman çizelgesi için Bulut sekmesi"
        progress.visibility = View.VISIBLE
        grid.adapter = albumAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 2
        lifecycleScope.launch {
            try {
                val folders = withContext(Dispatchers.IO) { api.listCloudPhotoAlbums() }
                // Reuse album tiles with cloud folder metadata via synthetic MediaAlbum
                val tiles = folders.map { f ->
                    MediaAlbum(
                        id = "cloud:${f.id}",
                        name = f.name,
                        count = 0,
                        coverUri = null,
                    )
                }
                albumAdapter.submit(tiles, cloudMode = true) { album ->
                    val id = album.id.removePrefix("cloud:")
                    val entry = folders.firstOrNull { it.id == id } ?: return@submit
                    viewingCloudAlbum = entry
                    loadCloudAlbumContents(entry)
                }
                subtitle.text = "${folders.size} bulut albümü · dokunarak aç · üstte zaman çizelgesi için Bulut"
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun loadCloudAlbumContents(folder: FileEntry) {
        title.text = folder.name
        subtitle.text = "Bulut albümü"
        progress.visibility = View.VISIBLE
        grid.adapter = cloudAdapter
        (grid.layoutManager as GridLayoutManager).spanCount = 3
        lifecycleScope.launch {
            try {
                // Flatten under this album folder
                val queue = ArrayDeque<String>()
                queue.add(folder.id)
                val out = ArrayList<FileEntry>()
                var guard = 0
                while (queue.isNotEmpty() && out.size < 800 && guard < 300) {
                    guard++
                    val id = queue.removeFirst()
                    val children = withContext(Dispatchers.IO) { api.listFiles(id) }
                    for (c in children) {
                        if (c.kind == "folder") queue.add(c.id)
                        else out += c
                    }
                }
                cloudAdapter.submit(out)
                subtitle.text = "${out.size} öğe · ${folder.name}"
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
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
            arrayOf(
                Manifest.permission.READ_MEDIA_IMAGES,
                Manifest.permission.READ_MEDIA_VIDEO,
            )
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

    private class MediaGridAdapter(
        private val onOpen: (LocalMedia) -> Unit,
    ) : RecyclerView.Adapter<MediaGridAdapter.VH>() {
        private var items: List<LocalMedia> = emptyList()

        fun submit(next: List<LocalMedia>) {
            items = next
            notifyDataSetChanged()
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
            val v = LayoutInflater.from(parent.context).inflate(R.layout.item_photo_cell, parent, false)
            return VH(v)
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(holder: VH, position: Int) {
            val item = items[position]
            holder.badge.visibility = if (item.isVideo) View.VISIBLE else View.GONE
            if (item.isVideo) holder.badge.text = "▶"
            holder.thumb.load(item.uri) {
                size(280, 280)
                crossfade(true)
            }
            holder.itemView.setOnClickListener { onOpen(item) }
        }

        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val thumb: ImageView = view.findViewById(R.id.photoThumb)
            val badge: TextView = view.findViewById(R.id.photoBadge)
        }
    }

    private class AlbumGridAdapter(
        private val defaultOpen: (MediaAlbum) -> Unit,
    ) : RecyclerView.Adapter<AlbumGridAdapter.VH>() {
        private var items: List<MediaAlbum> = emptyList()
        private var openHandler: (MediaAlbum) -> Unit = defaultOpen

        fun submit(next: List<MediaAlbum>, cloudMode: Boolean = false, onOpen: ((MediaAlbum) -> Unit)? = null) {
            items = next
            openHandler = onOpen ?: defaultOpen
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
            holder.itemView.setOnClickListener { openHandler(album) }
        }

        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val cover: ImageView = view.findViewById(R.id.albumCover)
            val name: TextView = view.findViewById(R.id.albumName)
            val count: TextView = view.findViewById(R.id.albumCount)
        }
    }

    private class CloudGridAdapter(
        private val onOpen: (FileEntry) -> Unit,
    ) : RecyclerView.Adapter<CloudGridAdapter.VH>() {
        private var items: List<FileEntry> = emptyList()
        private var token: String = ""
        private var serverUrl: String = ""

        fun updateAuth(token: String, serverUrl: String) {
            this.token = token
            this.serverUrl = serverUrl.trimEnd('/')
        }

        fun submit(next: List<FileEntry>) {
            items = next
            notifyDataSetChanged()
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
            val v = LayoutInflater.from(parent.context).inflate(R.layout.item_photo_cell, parent, false)
            return VH(v)
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(holder: VH, position: Int) {
            val item = items[position]
            holder.thumb.setImageDrawable(null)
            holder.badge.visibility = View.GONE
            holder.itemView.setOnClickListener { onOpen(item) }
            if (item.kind == "folder") {
                holder.thumb.setBackgroundColor(Color.parseColor("#0B5CAD"))
                holder.badge.visibility = View.VISIBLE
                holder.badge.text = "Albüm"
                return
            }
            holder.thumb.setBackgroundColor(Color.parseColor("#DCE6F5"))
            val mime = MainActivity.resolveMime(item)
            if (mime.startsWith("video/")) {
                holder.badge.visibility = View.VISIBLE
                holder.badge.text = "▶"
            }
            if ((mime.startsWith("image/") || mime.startsWith("video/")) &&
                token.isNotBlank() && serverUrl.isNotBlank()
            ) {
                val url = "$serverUrl/api/files/download/${item.id}?inline=1"
                holder.thumb.load(url) {
                    headers(Headers.headersOf("Authorization", "Bearer $token"))
                    size(280, 280)
                    crossfade(true)
                }
            }
        }

        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val thumb: ImageView = view.findViewById(R.id.photoThumb)
            val badge: TextView = view.findViewById(R.id.photoBadge)
        }
    }
}
