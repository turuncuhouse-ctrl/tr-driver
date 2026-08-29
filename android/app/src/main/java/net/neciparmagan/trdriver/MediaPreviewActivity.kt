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

        val image = findViewById<ImageView>(R.id.previewImage)
        val video = findViewById<VideoView>(R.id.previewVideo)
        videoView = video

        if (dataSource == null) {
            Toast.makeText(this, "Önizleme kaynağı yok", Toast.LENGTH_LONG).show()
            finish()
            return
        }

        when {
            mime.startsWith("image/") -> {
                image.visibility = View.VISIBLE
                val req = ImageRequest.Builder(this)
                    .data(dataSource)
                    .target(image)
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
                val contentUri = androidx.core.content.FileProvider.getUriForFile(
                    this@MediaPreviewActivity,
                    "${packageName}.files",
                    file,
                )
                shareUri = contentUri
                startActivity(
                    Intent.createChooser(
                        Intent(Intent.ACTION_SEND).apply {
                            type = mime.ifBlank { "*/*" }
                            putExtra(Intent.EXTRA_STREAM, contentUri)
                            putExtra(Intent.EXTRA_SUBJECT, displayName)
                            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                        },
                        "Paylaş",
                    ),
                )
            } catch (e: Exception) {
                Toast.makeText(this@MediaPreviewActivity, e.message ?: "İndirme başarısız", Toast.LENGTH_LONG).show()
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
