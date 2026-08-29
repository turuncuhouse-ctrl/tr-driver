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
import coil.imageLoader
import coil.request.ImageRequest
import net.neciparmagan.trdriver.data.SessionStore

class MediaPreviewActivity : AppCompatActivity() {
    private var videoView: VideoView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_media_preview)

        val id = intent.getStringExtra(EXTRA_ID).orEmpty()
        val name = intent.getStringExtra(EXTRA_NAME).orEmpty()
        val localUri = intent.getStringExtra(EXTRA_LOCAL_URI)?.let { runCatching { Uri.parse(it) }.getOrNull() }
        var mime = intent.getStringExtra(EXTRA_MIME).orEmpty()
        if (mime.isBlank() || mime == "application/octet-stream") {
            mime = guessMimeFromName(name)
        }
        val session = SessionStore(this)
        val remoteUrl = if (id.isNotBlank()) {
            session.serverUrl.trimEnd('/') + "/api/files/download/$id?inline=1"
        } else {
            null
        }
        val token = session.token.orEmpty()
        val dataSource: Any? = localUri ?: remoteUrl

        findViewById<TextView>(R.id.previewTitle).text = name
        findViewById<View>(R.id.btnClosePreview).setOnClickListener { finish() }

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
                    PlayerActivity.start(this, name, remoteUrl, token)
                } else {
                    Toast.makeText(this, "Yerel ses önizlemesi yok", Toast.LENGTH_SHORT).show()
                }
                finish()
            }
            else -> Toast.makeText(this, "Bu tür önizlenemiyor", Toast.LENGTH_SHORT).show()
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
