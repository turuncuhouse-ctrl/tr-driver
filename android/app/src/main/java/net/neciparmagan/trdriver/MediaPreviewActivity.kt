package net.neciparmagan.trdriver

import android.content.Intent
import android.media.AudioAttributes
import android.media.MediaPlayer
import android.net.Uri
import android.os.Bundle
import android.view.View
import android.widget.ImageView
import android.widget.TextView
import android.widget.Toast
import android.widget.VideoView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import coil.imageLoader
import coil.request.ImageRequest
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

class MediaPreviewActivity : AppCompatActivity() {
    private var player: MediaPlayer? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_media_preview)

        val id = intent.getStringExtra(EXTRA_ID).orEmpty()
        val name = intent.getStringExtra(EXTRA_NAME).orEmpty()
        val mime = intent.getStringExtra(EXTRA_MIME).orEmpty()
        val session = SessionStore(this)
        val url = session.serverUrl.trimEnd('/') + "/api/files/download/$id?inline=1"

        findViewById<TextView>(R.id.previewTitle).text = name
        findViewById<View>(R.id.btnClosePreview).setOnClickListener { finish() }

        val image = findViewById<ImageView>(R.id.previewImage)
        val video = findViewById<VideoView>(R.id.previewVideo)
        val audioHint = findViewById<TextView>(R.id.previewAudioHint)

        when {
            mime.startsWith("image/") -> {
                image.visibility = View.VISIBLE
                val token = session.token.orEmpty()
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
                // VideoView can't easily set Authorization; stream via temp redirect using headers proxy.
                playAuthorizedStream(url, session.token.orEmpty()) { local ->
                    video.setVideoURI(Uri.parse(local))
                    video.setOnPreparedListener { it.isLooping = false; video.start() }
                }
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

    private fun playAuthorizedStream(url: String, token: String, onReady: (String) -> Unit) {
        Thread {
            try {
                val client = OkHttpClient.Builder()
                    .connectTimeout(30, TimeUnit.SECONDS)
                    .readTimeout(5, TimeUnit.MINUTES)
                    .build()
                val req = Request.Builder().url(url).header("Authorization", "Bearer $token").build()
                client.newCall(req).execute().use { resp ->
                    if (!resp.isSuccessful) throw IllegalStateException(resp.message)
                    val file = cacheDir.resolve("preview-${System.currentTimeMillis()}.bin")
                    resp.body!!.byteStream().use { input -> file.outputStream().use { input.copyTo(it) } }
                    runOnUiThread { onReady(file.toURI().toString()) }
                }
            } catch (e: Exception) {
                runOnUiThread {
                    Toast.makeText(this, "Önizleme açılamadı: ${e.message}", Toast.LENGTH_LONG).show()
                }
            }
        }.start()
    }

    override fun onDestroy() {
        player?.release()
        player = null
        super.onDestroy()
    }

    companion object {
        const val EXTRA_ID = "id"
        const val EXTRA_NAME = "name"
        const val EXTRA_MIME = "mime"
    }
}
