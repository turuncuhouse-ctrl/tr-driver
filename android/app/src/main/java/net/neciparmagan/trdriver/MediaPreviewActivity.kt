package net.neciparmagan.trdriver

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.view.View
import android.widget.ImageView
import android.widget.MediaController
import android.widget.TextView
import android.widget.Toast
import android.widget.VideoView
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import coil.imageLoader
import coil.request.ImageRequest
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import java.io.File

class MediaPreviewActivity : AppCompatActivity() {
    private var videoView: VideoView? = null
    private var localUri: Uri? = null
    private var remoteId: String = ""
    private var mime: String = ""
    private var displayName: String = ""
    private var shareUri: Uri? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_media_preview)

        remoteId = intent.getStringExtra(EXTRA_ID).orEmpty()
        displayName = intent.getStringExtra(EXTRA_NAME).orEmpty()
        localUri = intent.getStringExtra(EXTRA_LOCAL_URI)?.let { runCatching { Uri.parse(it) }.getOrNull() }
        mime = intent.getStringExtra(EXTRA_MIME).orEmpty()
        if (mime.isBlank() || mime == "application/octet-stream") {
            mime = guessMimeFromName(displayName)
        }
        val session = SessionStore(this)
        val remoteUrl = if (remoteId.isNotBlank()) {
            session.serverUrl.trimEnd('/') + "/api/files/download/$remoteId?inline=1"
        } else {
            null
        }
        val token = session.token.orEmpty()
        val dataSource: Any? = localUri ?: remoteUrl
        shareUri = localUri

        findViewById<TextView>(R.id.previewTitle).text = displayName
        findViewById<View>(R.id.btnClosePreview).setOnClickListener { finish() }
        findViewById<View>(R.id.btnSharePreview).setOnClickListener { shareCurrent(session) }
        findViewById<View>(R.id.btnOpenExternal).setOnClickListener { openExternal() }
        findViewById<View>(R.id.btnInfoPreview).setOnClickListener {
            val sizeHint = localUri?.let { uri ->
                runCatching {
                    contentResolver.openAssetFileDescriptor(uri, "r")?.use { it.length }
                }.getOrNull()
            }
            val msg = buildString {
                append("Ad: $displayName\n")
                append("Tür: ${mime.ifBlank { "bilinmiyor" }}\n")
                if (sizeHint != null && sizeHint > 0) {
                    append("Boyut: ${SessionStore.formatBytes(sizeHint)}\n")
                }
                if (localUri != null) append("Kaynak: cihaz")
                else if (remoteId.isNotBlank()) append("Kaynak: bulut")
            }
            androidx.appcompat.app.AlertDialog.Builder(this)
                .setTitle("Bilgi")
                .setMessage(msg)
                .setPositiveButton("Tamam", null)
                .show()
        }
        val deleteBtn = findViewById<View>(R.id.btnDeletePreview)
        if (localUri != null) {
            deleteBtn.visibility = View.VISIBLE
            deleteBtn.setOnClickListener {
                androidx.appcompat.app.AlertDialog.Builder(this)
                    .setTitle("Silinsin mi?")
                    .setMessage(displayName)
                    .setPositiveButton("Sil") { _, _ ->
                        val uri = localUri!!
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
        } else {
            deleteBtn.visibility = View.GONE
        }

        val image = findViewById<ImageView>(R.id.previewImage)
        val video = findViewById<VideoView>(R.id.previewVideo)
        videoView = video

        if (dataSource == null) {
            Toast.makeText(this, "Önizleme kaynağı yok", Toast.LENGTH_LONG).show()
            finish()
            return
        }

        when {
            mime.startsWith("image/") || mime == "application/octet-stream" || mime.isBlank() -> {
                image.visibility = View.VISIBLE
                val req = ImageRequest.Builder(this)
                    .data(dataSource)
                    .target(image)
                    .listener(
                        onError = { _, _ ->
                            findViewById<TextView>(R.id.previewAudioHint).apply {
                                visibility = View.VISIBLE
                                text = "Önizleme yüklenemedi.\n$displayName\n\n\"Birlikte aç\" ile deneyin."
                            }
                        },
                    )
                if (localUri == null && token.isNotBlank()) {
                    req.addHeader("Authorization", "Bearer $token")
                }
                imageLoader.enqueue(req.build())
            }
            mime.startsWith("video/") -> {
                video.visibility = View.VISIBLE
                val controller = MediaController(this)
                controller.setAnchorView(video)
                video.setMediaController(controller)
                video.setOnPreparedListener { mp ->
                    mp.isLooping = false
                    video.start()
                }
                video.setOnErrorListener { _, what, extra ->
                    Toast.makeText(this, "Video oynatılamadı ($what/$extra)", Toast.LENGTH_LONG).show()
                    true
                }
                if (localUri != null) {
                    video.setVideoURI(localUri)
                } else {
                    val headers = mapOf("Authorization" to "Bearer $token")
                    video.setVideoURI(Uri.parse(remoteUrl), headers)
                }
                video.requestFocus()
            }
            mime.startsWith("audio/") -> {
                if (remoteUrl != null) {
                    PlayerActivity.start(this, displayName, remoteUrl, token)
                } else {
                    Toast.makeText(this, "Yerel ses önizlemesi yok", Toast.LENGTH_SHORT).show()
                }
                finish()
            }
            else -> Toast.makeText(this, "Bu tür önizlenemiyor", Toast.LENGTH_SHORT).show()
        }
    }

    private fun shareCurrent(session: SessionStore) {
        val uri = shareUri
        if (uri != null && localUri != null) {
            GalleryShareHelper.showLocalShareSheet(
                this,
                listOf(
                    net.neciparmagan.trdriver.data.LocalMedia(
                        mediaKey = uri.toString(),
                        uri = uri,
                        displayName = displayName.ifBlank { "paylas" },
                        mimeType = mime.ifBlank { "*/*" },
                        sizeBytes = 0L,
                        dateTakenMs = 0L,
                        isVideo = mime.startsWith("video/"),
                    ),
                ),
            )
            return
        }
        if (uri != null) {
            startActivity(
                Intent.createChooser(
                    Intent(Intent.ACTION_SEND).apply {
                        type = mime.ifBlank { "*/*" }
                        putExtra(Intent.EXTRA_STREAM, uri)
                        putExtra(Intent.EXTRA_SUBJECT, displayName)
                        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    },
                    "Paylaş",
                ),
            )
            return
        }
        if (remoteId.isBlank() || !session.isLoggedIn) {
            Toast.makeText(this, "Paylaşılacak dosya yok", Toast.LENGTH_SHORT).show()
            return
        }
        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Paylaş")
            .setItems(arrayOf("İndirip uygulamalarla paylaş", "Bağlantı oluştur", "Bağlantıyı kopyala")) { _, which ->
                when (which) {
                    0 -> downloadThenShare(session)
                    1 -> createPreviewLink(session, copyOnly = false)
                    2 -> createPreviewLink(session, copyOnly = true)
                }
            }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun downloadThenShare(session: SessionStore) {
        Toast.makeText(this, "Paylaşım için indiriliyor…", Toast.LENGTH_SHORT).show()
        lifecycleScope.launch {
            try {
                val api = DriveApi(session, applicationContext)
                val file = withContext(Dispatchers.IO) {
                    api.downloadToCache(
                        net.neciparmagan.trdriver.data.FileEntry(
                            id = remoteId,
                            name = displayName.ifBlank { "share.bin" },
                            kind = "file",
                            mimeType = mime,
                        ),
                    )
                }
                GalleryShareHelper.shareFile(this@MediaPreviewActivity, file, displayName, mime)
            } catch (e: Exception) {
                Toast.makeText(this@MediaPreviewActivity, e.message ?: "İndirme başarısız", Toast.LENGTH_LONG).show()
            }
        }
    }

    private fun createPreviewLink(session: SessionStore, copyOnly: Boolean) {
        lifecycleScope.launch {
            try {
                val api = DriveApi(session, applicationContext)
                val resp = withContext(Dispatchers.IO) { api.createShareLink(remoteId) }
                val url = resp.url.ifBlank { throw IllegalStateException("Boş bağlantı") }
                if (copyOnly) GalleryShareHelper.copyText(this@MediaPreviewActivity, url)
                else GalleryShareHelper.shareTextLink(this@MediaPreviewActivity, url, displayName)
            } catch (e: Exception) {
                Toast.makeText(this@MediaPreviewActivity, e.message ?: "Bağlantı oluşturulamadı", Toast.LENGTH_LONG).show()
            }
        }
    }

    private fun openExternal() {
        val uri = shareUri ?: localUri
        if (uri == null) {
            Toast.makeText(this, "Önce paylaşım için indirin veya yerel dosya açın", Toast.LENGTH_SHORT).show()
            return
        }
        try {
            startActivity(
                Intent.createChooser(
                    Intent(Intent.ACTION_VIEW).apply {
                        setDataAndType(uri, mime.ifBlank { "*/*" })
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
        videoView?.pause()
        super.onPause()
    }

    override fun onDestroy() {
        videoView?.stopPlayback()
        videoView = null
        super.onDestroy()
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
