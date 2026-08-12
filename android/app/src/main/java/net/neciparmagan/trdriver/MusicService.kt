package net.neciparmagan.trdriver

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.media.AudioAttributes
import android.media.MediaPlayer
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.File
import java.util.concurrent.TimeUnit

class MusicService : Service() {
    private var player: MediaPlayer? = null
    private var title: String = "TR Driver Müzik"

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopPlayback()
                stopSelf()
                return START_NOT_STICKY
            }
            ACTION_PLAY -> {
                val url = intent.getStringExtra(EXTRA_URL).orEmpty()
                val token = intent.getStringExtra(EXTRA_TOKEN).orEmpty()
                title = intent.getStringExtra(EXTRA_TITLE) ?: title
                startForeground(NOTIF_ID, buildNotification("Hazırlanıyor…"))
                Thread { playRemote(url, token) }.start()
            }
        }
        return START_STICKY
    }

    private fun playRemote(url: String, token: String) {
        try {
            val client = OkHttpClient.Builder()
                .connectTimeout(30, TimeUnit.SECONDS)
                .readTimeout(10, TimeUnit.MINUTES)
                .build()
            val req = Request.Builder().url(url).header("Authorization", "Bearer $token").build()
            client.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) throw IllegalStateException(resp.message)
                val file = File(cacheDir, "music-now.playing")
                resp.body!!.byteStream().use { input -> file.outputStream().use { input.copyTo(it) } }
                runCatching { player?.release() }
                player = MediaPlayer().apply {
                    setAudioAttributes(
                        AudioAttributes.Builder()
                            .setUsage(AudioAttributes.USAGE_MEDIA)
                            .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                            .build()
                    )
                    setDataSource(file.absolutePath)
                    setOnCompletionListener {
                        stopPlayback()
                        stopSelf()
                    }
                    prepare()
                    start()
                }
                val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
                nm.notify(NOTIF_ID, buildNotification(title))
            }
        } catch (e: Exception) {
            val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
            nm.notify(NOTIF_ID, buildNotification("Çalınamadı: ${e.message}"))
            stopSelf()
        }
    }

    private fun stopPlayback() {
        runCatching {
            player?.stop()
            player?.release()
        }
        player = null
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    private fun buildNotification(content: String): Notification {
        val channelId = "trdriver_music"
        if (Build.VERSION.SDK_INT >= 26) {
            val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
            nm.createNotificationChannel(
                NotificationChannel(channelId, "TR Driver Müzik", NotificationManager.IMPORTANCE_LOW)
            )
        }
        val stop = PendingIntent.getService(
            this,
            1,
            Intent(this, MusicService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, channelId)
            .setContentTitle("TR Driver")
            .setContentText(content)
            .setSmallIcon(R.drawable.ic_launcher)
            .setOngoing(true)
            .addAction(0, "Durdur", stop)
            .build()
    }

    override fun onDestroy() {
        stopPlayback()
        super.onDestroy()
    }

    companion object {
        const val ACTION_PLAY = "net.neciparmagan.trdriver.PLAY"
        const val ACTION_STOP = "net.neciparmagan.trdriver.STOP"
        const val EXTRA_URL = "url"
        const val EXTRA_TITLE = "title"
        const val EXTRA_TOKEN = "token"
        private const val NOTIF_ID = 42
    }
}
