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
import androidx.core.content.ContextCompat
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
        var mime = intent.getStringExtra(EXTRA_MIME).orEmpty()
        if (mime.isBlank() || mime == "application/octet-stream") {
            mime = guessMimeFromName(name)
        }
        val session = SessionStore(this)
        val url = session.serverUrl.trimEnd('/') + "/api/files/download/$id?inline=1"
        val token = session.token.orEmpty()

        findViewById<TextView>(R.id.previewTitle).text = name
        findViewById<View>(R.id.btnClosePreview).setOnClickListener { finish() }

        val image = findViewById<ImageView>(R.id.previewImage)
        val video = findViewById<VideoView>(R.id.previewVideo)
        val audioHint = findViewById<TextView>(R.id.previewAudioHint)
        videoView = video

        when {
            mime.startsWith("image/") -> {
                image.visibility = View.VISIBLE
                imageLoader.enqueue(
                    ImageRequest.Builder(this)
                        .data(url)
                        .addHeader("Authorization", "Bearer $token")
                        .target(image)
                        .build(),
                )
            }
            mime.startsWith("video/") -> {
                video.visibility = View.VISIBLE
                val headers = mapOf("Authorization" to "Bearer $token")
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
                video.setVideoURI(Uri.parse(url), headers)
                video.requestFocus()
            }
            mime.startsWith("audio/") -> {
                audioHint.visibility = View.VISIBLE
                audioHint.text = "Müzik çalınıyor…\nEkran kapalıyken devam için bildirim kullanılır."
                val service = Intent(this, MusicService::class.java).apply {
                    action = MusicService.ACTION_PLAY
                    putExtra(MusicService.EXTRA_URL, url)
                    putExtra(MusicService.EXTRA_TITLE, name)
                    putExtra(MusicService.EXTRA_TOKEN, session.token)
                }
                ContextCompat.startForegroundService(this, service)
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

        fun guessMimeFromName(name: String): String {
            val ext = name.substringAfterLast('.', "").lowercase()
            return when (ext) {
                "jpg", "jpeg" -> "image/jpeg"
                "png" -> "image/png"
                "gif" -> "image/gif"
                "webp" -> "image/webp"
                "mp4", "m4v" -> "video/mp4"
                "webm" -> "video/webm"
                "mkv" -> "video/x-matroska"
                "3gp" -> "video/3gpp"
                "mp3" -> "audio/mpeg"
                "m4a", "aac" -> "audio/mp4"
                "wav" -> "audio/wav"
                "ogg", "oga" -> "audio/ogg"
                else -> "application/octet-stream"
            }
        }
    }
}
