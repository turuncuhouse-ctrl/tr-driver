package net.neciparmagan.trdriver

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.MediaController
import android.widget.TextView
import android.widget.Toast
import android.widget.VideoView
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.RecyclerView
import androidx.viewpager2.widget.ViewPager2
import coil.imageLoader
import coil.request.ImageRequest
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore

class MediaPreviewActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var pager: ViewPager2
    private lateinit var title: TextView
    private lateinit var counter: TextView
    private lateinit var deleteBtn: View
    private var items: List<GalleryPreviewItem> = emptyList()
    private var currentIndex = 0
    private var activeVideo: VideoView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_media_preview)
        session = SessionStore(this)

        items = GalleryPreviewSession.items
        currentIndex = GalleryPreviewSession.startIndex
        if (items.isEmpty()) {
            val single = itemFromIntent()
            if (single == null) {
                Toast.makeText(this, "Önizleme kaynağı yok", Toast.LENGTH_LONG).show()
                finish()
                return
            }
            items = listOf(single)
            currentIndex = 0
        }
        currentIndex = currentIndex.coerceIn(0, items.lastIndex)

        title = findViewById(R.id.previewTitle)
        counter = findViewById(R.id.previewCounter)
        deleteBtn = findViewById(R.id.btnDeletePreview)
        pager = findViewById(R.id.previewPager)
        pager.adapter = PreviewPagerAdapter(items)
        pager.setCurrentItem(currentIndex, false)
        pager.registerOnPageChangeCallback(object : ViewPager2.OnPageChangeCallback() {
            override fun onPageSelected(position: Int) {
                currentIndex = position
                GalleryPreviewSession.startIndex = position
                bindChrome()
            }
        })

        findViewById<View>(R.id.btnClosePreview).setOnClickListener { finish() }
        findViewById<View>(R.id.btnSharePreview).setOnClickListener { shareCurrent() }
        findViewById<View>(R.id.btnOpenExternal).setOnClickListener { openExternal() }
        findViewById<View>(R.id.btnInfoPreview).setOnClickListener { showInfo() }
        deleteBtn.setOnClickListener { confirmDelete() }
        bindChrome()
    }

    private fun itemFromIntent(): GalleryPreviewItem? {
        val name = intent.getStringExtra(EXTRA_NAME).orEmpty()
        val mime = intent.getStringExtra(EXTRA_MIME).orEmpty().ifBlank { guessMimeFromName(name) }
        val local = intent.getStringExtra(EXTRA_LOCAL_URI)
        val remote = intent.getStringExtra(EXTRA_ID)
        if (local.isNullOrBlank() && remote.isNullOrBlank()) return null
        return GalleryPreviewItem(
            key = remote ?: local.orEmpty(),
            name = name,
            mime = mime,
            localUri = local,
            remoteId = remote,
            isVideo = mime.startsWith("video/"),
        )
    }

    private fun current(): GalleryPreviewItem? = items.getOrNull(currentIndex)

    private fun bindChrome() {
        val item = current() ?: return
        title.text = item.name
        counter.text = if (items.size > 1) "${currentIndex + 1} / ${items.size}" else ""
        deleteBtn.visibility = if (item.localUri != null) View.VISIBLE else View.GONE
    }

    private fun showInfo() {
        val item = current() ?: return
        val local = item.parsedLocalUri()
        val sizeHint = local?.let { uri ->
            runCatching { contentResolver.openAssetFileDescriptor(uri, "r")?.use { it.length } }.getOrNull()
        }
        val msg = buildString {
            append("Ad: ${item.name}\n")
            append("Tür: ${item.mime.ifBlank { "bilinmiyor" }}\n")
            if (sizeHint != null && sizeHint > 0) {
                append("Boyut: ${SessionStore.formatBytes(sizeHint)}\n")
            }
            when {
                local != null && !item.remoteId.isNullOrBlank() ->
                    append("Kaynak: cihaz + bulut (mobil veri ile açılabilir)")
                local != null -> append("Kaynak: cihaz")
                else -> append("Kaynak: bulut (Wi‑Fi veya mobil veri)")
            }
        }
        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Bilgi")
            .setMessage(msg)
            .setPositiveButton("Tamam", null)
            .show()
    }

    private fun confirmDelete() {
        val item = current() ?: return
        val uri = item.parsedLocalUri() ?: return
        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Silinsin mi?")
            .setMessage(item.name)
            .setPositiveButton("Sil") { _, _ ->
                val rows = runCatching { contentResolver.delete(uri, null, null) }.getOrDefault(0)
                if (rows > 0) {
                    Toast.makeText(this, "Silindi", Toast.LENGTH_SHORT).show()
                    setResult(RESULT_OK)
                    finish()
                } else {
                    Toast.makeText(this, "Silinemedi (izin gerekebilir)", Toast.LENGTH_LONG).show()
                }
            }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun shareCurrent() {
        val item = current() ?: return
        val local = item.parsedLocalUri()
        if (local != null) {
            GalleryShareHelper.showLocalShareSheet(
                this,
                listOf(
                    net.neciparmagan.trdriver.data.LocalMedia(
                        mediaKey = item.key,
                        uri = local,
                        displayName = item.name.ifBlank { "paylas" },
                        mimeType = item.mime.ifBlank { "*/*" },
                        sizeBytes = 0L,
                        dateTakenMs = 0L,
                        isVideo = item.isVideo,
                    ),
                ),
            )
            return
        }
        if (item.remoteId.isNullOrBlank() || !session.isLoggedIn) {
            Toast.makeText(this, "Paylaşılacak dosya yok", Toast.LENGTH_SHORT).show()
            return
        }
        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Paylaş")
            .setItems(arrayOf("İndirip uygulamalarla paylaş", "Bağlantı oluştur", "Bağlantıyı kopyala")) { _, which ->
                when (which) {
                    0 -> downloadThenShare(item)
                    1 -> createPreviewLink(item, copyOnly = false)
                    2 -> createPreviewLink(item, copyOnly = true)
                }
            }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun downloadThenShare(item: GalleryPreviewItem) {
        val remoteId = item.remoteId ?: return
        Toast.makeText(this, "Paylaşım için indiriliyor…", Toast.LENGTH_SHORT).show()
        lifecycleScope.launch {
            try {
                val api = DriveApi(session, applicationContext)
                val file = withContext(Dispatchers.IO) {
                    api.downloadToCache(
                        net.neciparmagan.trdriver.data.FileEntry(
                            id = remoteId,
                            name = item.name.ifBlank { "share.bin" },
                            kind = "file",
                            mimeType = item.mime,
                        ),
                    )
                }
                GalleryShareHelper.shareFile(this@MediaPreviewActivity, file, item.name, item.mime)
            } catch (e: Exception) {
                Toast.makeText(this@MediaPreviewActivity, e.message ?: "İndirme başarısız", Toast.LENGTH_LONG).show()
            }
        }
    }

    private fun createPreviewLink(item: GalleryPreviewItem, copyOnly: Boolean) {
        val remoteId = item.remoteId ?: return
        lifecycleScope.launch {
            try {
                val api = DriveApi(session, applicationContext)
                val resp = withContext(Dispatchers.IO) { api.createShareLink(remoteId) }
                val url = resp.url.ifBlank { throw IllegalStateException("Boş bağlantı") }
                if (copyOnly) GalleryShareHelper.copyText(this@MediaPreviewActivity, url)
                else GalleryShareHelper.shareTextLink(this@MediaPreviewActivity, url, item.name)
            } catch (e: Exception) {
                Toast.makeText(this@MediaPreviewActivity, e.message ?: "Bağlantı oluşturulamadı", Toast.LENGTH_LONG).show()
            }
        }
    }

    private fun openExternal() {
        val item = current() ?: return
        val uri = item.parsedLocalUri()
        if (uri == null) {
            Toast.makeText(this, "Yerel dosya yok — önce indirin veya buluttan bakın", Toast.LENGTH_SHORT).show()
            return
        }
        try {
            startActivity(
                Intent.createChooser(
                    Intent(Intent.ACTION_VIEW).apply {
                        setDataAndType(uri, item.mime.ifBlank { "*/*" })
                        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    },
                    "Birlikte aç",
                ),
            )
        } catch (e: Exception) {
            Toast.makeText(this, e.message, Toast.LENGTH_LONG).show()
        }
    }

    override fun onPause() {
        activeVideo?.pause()
        super.onPause()
    }

    override fun onDestroy() {
        activeVideo?.stopPlayback()
        activeVideo = null
        super.onDestroy()
    }

    private inner class PreviewPagerAdapter(
        private val pages: List<GalleryPreviewItem>,
    ) : RecyclerView.Adapter<PageVH>() {
        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): PageVH {
            val v = LayoutInflater.from(parent.context).inflate(R.layout.item_media_preview_page, parent, false)
            return PageVH(v)
        }

        override fun getItemCount(): Int = pages.size

        override fun onBindViewHolder(holder: PageVH, position: Int) {
            bindPage(holder, pages[position])
        }

        override fun onViewRecycled(holder: PageVH) {
            holder.video.stopPlayback()
            holder.video.visibility = View.GONE
            if (activeVideo === holder.video) activeVideo = null
            holder.image.setImageDrawable(null)
        }

        private fun bindPage(holder: PageVH, item: GalleryPreviewItem) {
            holder.hint.visibility = View.GONE
            holder.image.visibility = View.GONE
            holder.video.visibility = View.GONE
            holder.video.stopPlayback()
            val token = session.token.orEmpty()
            val remoteUrl = if (!item.remoteId.isNullOrBlank() && session.serverUrl.isNotBlank()) {
                session.serverUrl.trimEnd('/') + "/api/files/download/${item.remoteId}?inline=1"
            } else {
                null
            }
            val local = item.parsedLocalUri()?.takeIf { uriReadable(it) }
            when {
                item.mime.startsWith("video/") -> {
                    holder.video.visibility = View.VISIBLE
                    val controller = MediaController(this@MediaPreviewActivity)
                    controller.setAnchorView(holder.video)
                    holder.video.setMediaController(controller)
                    holder.video.setOnPreparedListener { mp ->
                        mp.isLooping = false
                        if (pager.currentItem == holder.bindingAdapterPosition) holder.video.start()
                    }
                    holder.video.setOnErrorListener { _, _, _ ->
                        if (remoteUrl != null && local != null) {
                            playRemoteVideo(holder, remoteUrl, token)
                            true
                        } else {
                            holder.hint.visibility = View.VISIBLE
                            holder.hint.text = "Video oynatılamadı.\n${item.name}"
                            true
                        }
                    }
                    if (local != null) {
                        holder.video.setVideoURI(local)
                    } else if (remoteUrl != null) {
                        playRemoteVideo(holder, remoteUrl, token)
                    } else {
                        holder.hint.visibility = View.VISIBLE
                        holder.hint.text = "Video kaynağı yok"
                    }
                    if (pager.currentItem == holder.bindingAdapterPosition) {
                        activeVideo = holder.video
                    }
                }
                else -> {
                    holder.image.visibility = View.VISIBLE
                    val primary: Any? = local ?: remoteUrl
                    if (primary == null) {
                        holder.hint.visibility = View.VISIBLE
                        holder.hint.text = "Önizleme kaynağı yok"
                        return
                    }
                    loadImage(holder, item, primary, remoteUrl, token, usedFallback = local == null)
                }
            }
        }

        private fun playRemoteVideo(holder: PageVH, remoteUrl: String, token: String) {
            holder.hint.visibility = View.VISIBLE
            holder.hint.text = "Buluttan açılıyor (mobil veri kullanılabilir)…"
            try {
                val headers = if (token.isNotBlank()) mapOf("Authorization" to "Bearer $token") else emptyMap()
                if (headers.isEmpty()) holder.video.setVideoURI(Uri.parse(remoteUrl))
                else holder.video.setVideoURI(Uri.parse(remoteUrl), headers)
            } catch (e: Exception) {
                holder.hint.text = "Bulut video açılamadı: ${e.message}"
            }
        }

        private fun loadImage(
            holder: PageVH,
            item: GalleryPreviewItem,
            data: Any,
            remoteUrl: String?,
            token: String,
            usedFallback: Boolean,
        ) {
            if (usedFallback && remoteUrl != null) {
                holder.hint.visibility = View.VISIBLE
                holder.hint.text = "Buluttan açılıyor…"
            }
            val req = ImageRequest.Builder(this@MediaPreviewActivity)
                .data(data)
                .target(holder.image)
                .listener(
                    onSuccess = { _, _ ->
                        if (usedFallback) holder.hint.visibility = View.GONE
                    },
                    onError = { _, _ ->
                        if (!usedFallback && remoteUrl != null) {
                            loadImage(holder, item, remoteUrl, remoteUrl, token, usedFallback = true)
                        } else {
                            holder.hint.visibility = View.VISIBLE
                            holder.hint.text = "Önizleme yüklenemedi.\n${item.name}"
                        }
                    },
                )
            if (data is String && token.isNotBlank()) {
                req.addHeader("Authorization", "Bearer $token")
            }
            imageLoader.enqueue(req.build())
        }
    }

    private class PageVH(view: View) : RecyclerView.ViewHolder(view) {
        val image: ImageView = view.findViewById(R.id.pageImage)
        val video: VideoView = view.findViewById(R.id.pageVideo)
        val hint: TextView = view.findViewById(R.id.pageHint)
    }

    private fun uriReadable(uri: Uri): Boolean {
        return runCatching {
            contentResolver.openAssetFileDescriptor(uri, "r")?.use { it.length >= 0 } == true
        }.getOrDefault(false)
    }

    companion object {
        const val EXTRA_ID = "id"
        const val EXTRA_NAME = "name"
        const val EXTRA_MIME = "mime"
        const val EXTRA_LOCAL_URI = "local_uri"

        fun guessMimeFromName(name: String): String {
            val ext = name.substringAfterLast('.', "").lowercase()
            return when (ext) {
                "jpg", "jpeg" -> "image/jpeg"
                "png" -> "image/png"
                "gif" -> "image/gif"
                "webp" -> "image/webp"
                "heic", "heif" -> "image/heic"
                "mp4", "m4v" -> "video/mp4"
                "webm" -> "video/webm"
                "mkv" -> "video/x-matroska"
                "3gp" -> "video/3gpp"
                "mp3" -> "audio/mpeg"
                "m4a", "aac" -> "audio/mp4"
                "wav" -> "audio/wav"
                "ogg", "oga" -> "audio/ogg"
                "flac" -> "audio/flac"
                "pdf" -> "application/pdf"
                else -> "application/octet-stream"
            }
        }
    }
}
