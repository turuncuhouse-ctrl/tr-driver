package net.neciparmagan.trdriver

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.media.AudioAttributes
import android.media.MediaPlayer
import android.net.Uri
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.support.v4.media.MediaMetadataCompat
import android.support.v4.media.session.MediaSessionCompat
import android.support.v4.media.session.PlaybackStateCompat
import androidx.core.app.NotificationCompat
import androidx.media.app.NotificationCompat.MediaStyle
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Foreground music player with lock-screen / notification controls
 * (play, pause, seek) — streams with Authorization header.
 */
class MusicService : Service(), MediaPlayer.OnPreparedListener, MediaPlayer.OnCompletionListener,
    MediaPlayer.OnErrorListener {

    private var player: MediaPlayer? = null
    private var session: MediaSessionCompat? = null
    private var title: String = "TR Driver"
    private var url: String = ""
    private var token: String = ""
    private val preparing = AtomicBoolean(false)

    private fun publishNowPlaying(playing: Boolean) {
        currentTitle = title
        currentUrl = url
        currentToken = token
        isPlaying = playing
        isSessionActive = true
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel()
        session = MediaSessionCompat(this, "TRDriverMusic").apply {
            setCallback(object : MediaSessionCompat.Callback() {
                override fun onPlay() = resume()
                override fun onPause() = pause()
                override fun onStop() = stopAll()
                override fun onSeekTo(pos: Long) {
                    runCatching { player?.seekTo(pos.toInt()) }
                    updateState()
                }
            })
            isActive = true
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopAll()
                return START_NOT_STICKY
            }
            ACTION_PAUSE -> {
                pause()
                return START_STICKY
            }
            ACTION_RESUME, ACTION_TOGGLE -> {
                if (player?.isPlaying == true) pause() else resume()
                return START_STICKY
            }
            ACTION_PLAY -> {
                url = intent.getStringExtra(EXTRA_URL).orEmpty()
                token = intent.getStringExtra(EXTRA_TOKEN).orEmpty()
                title = intent.getStringExtra(EXTRA_TITLE)?.ifBlank { "TR Driver" } ?: "TR Driver"
                publishNowPlaying(playing = false)
                startForeground(NOTIF_ID, buildNotification(playing = false, text = "Hazırlanıyor…"))
                startStream()
            }
            else -> {
                // Keep service alive if restarted
            }
        }
        return START_STICKY
    }

    private fun startStream() {
        if (url.isBlank()) {
            stopAll()
            return
        }
        if (!preparing.compareAndSet(false, true)) return
        runCatching {
            player?.reset()
            player?.release()
        }
        player = MediaPlayer().apply {
            setWakeMode(applicationContext, PowerManager.PARTIAL_WAKE_LOCK)
            setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                    .build(),
            )
            setOnPreparedListener(this@MusicService)
            setOnCompletionListener(this@MusicService)
            setOnErrorListener(this@MusicService)
            val headers = if (token.isNotBlank()) {
                mapOf("Authorization" to "Bearer $token")
            } else {
                emptyMap()
            }
            setDataSource(applicationContext, Uri.parse(url), headers)
            prepareAsync()
        }
    }

    override fun onPrepared(mp: MediaPlayer?) {
        preparing.set(false)
        mp?.start()
        session?.setMetadata(
            MediaMetadataCompat.Builder()
                .putString(MediaMetadataCompat.METADATA_KEY_TITLE, title)
                .putString(MediaMetadataCompat.METADATA_KEY_ARTIST, "TR Driver")
                .putLong(MediaMetadataCompat.METADATA_KEY_DURATION, mp?.duration?.toLong() ?: 0L)
                .build(),
        )
        publishNowPlaying(playing = true)
        updateState()
        notifyPlaying()
        broadcast(ACTION_STATE)
    }

    override fun onCompletion(mp: MediaPlayer?) {
        updateState(PlaybackStateCompat.STATE_STOPPED)
        stopAll()
    }

    override fun onError(mp: MediaPlayer?, what: Int, extra: Int): Boolean {
        preparing.set(false)
        isPlaying = false
        isSessionActive = false
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIF_ID, buildNotification(playing = false, text = "Çalınamadı ($what/$extra)"))
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
        broadcast(ACTION_STATE)
        return true
    }

    private fun pause() {
        runCatching { if (player?.isPlaying == true) player?.pause() }
        publishNowPlaying(playing = false)
        updateState()
        notifyPlaying()
        broadcast(ACTION_STATE)
    }

    private fun resume() {
        val p = player
        if (p == null) {
            if (url.isNotBlank()) startStream()
            return
        }
        runCatching { p.start() }
        publishNowPlaying(playing = true)
        updateState()
        notifyPlaying()
        broadcast(ACTION_STATE)
    }

    private fun stopAll() {
        preparing.set(false)
        runCatching {
            player?.stop()
            player?.release()
        }
        player = null
        session?.isActive = false
        session?.release()
        session = null
        isPlaying = false
        isSessionActive = false
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
        broadcast(ACTION_STATE)
    }

    private fun updateState(forced: Int? = null) {
        val p = player
        val state = forced ?: when {
            p == null -> PlaybackStateCompat.STATE_STOPPED
            p.isPlaying -> PlaybackStateCompat.STATE_PLAYING
            else -> PlaybackStateCompat.STATE_PAUSED
        }
        val pos = runCatching { p?.currentPosition?.toLong() ?: 0L }.getOrDefault(0L)
        session?.setPlaybackState(
            PlaybackStateCompat.Builder()
                .setActions(
                    PlaybackStateCompat.ACTION_PLAY or
                        PlaybackStateCompat.ACTION_PAUSE or
                        PlaybackStateCompat.ACTION_PLAY_PAUSE or
                        PlaybackStateCompat.ACTION_STOP or
                        PlaybackStateCompat.ACTION_SEEK_TO,
                )
                .setState(state, pos, if (state == PlaybackStateCompat.STATE_PLAYING) 1f else 0f)
                .build(),
        )
    }

    private fun notifyPlaying() {
        val playing = player?.isPlaying == true
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIF_ID, buildNotification(playing, title))
    }

    private fun buildNotification(playing: Boolean, text: String): Notification {
        val openPlayer = PendingIntent.getActivity(
            this,
            0,
            Intent(this, PlayerActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_URL, url)
                putExtra(EXTRA_TOKEN, token)
            },
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val toggle = PendingIntent.getService(
            this,
            2,
            Intent(this, MusicService::class.java).setAction(ACTION_TOGGLE),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            this,
            1,
            Intent(this, MusicService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val mediaSession = session
        val builder = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(title)
            .setContentText(if (playing) "Çalıyor · kilit ekranından kontrol" else text)
            .setSmallIcon(R.mipmap.ic_launcher)
            .setContentIntent(openPlayer)
            .setOngoing(playing)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setCategory(NotificationCompat.CATEGORY_TRANSPORT)
            .addAction(
                if (playing) android.R.drawable.ic_media_pause else android.R.drawable.ic_media_play,
                if (playing) "Duraklat" else "Çal",
                toggle,
            )
            .addAction(android.R.drawable.ic_menu_close_clear_cancel, "Durdur", stop)
        if (mediaSession != null) {
            builder.setStyle(
                MediaStyle()
                    .setMediaSession(mediaSession.sessionToken)
                    .setShowActionsInCompactView(0, 1),
            )
        }
        return builder.build()
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "TR Driver Müzik", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "Müzik çalma ve kilit ekranı kontrolleri"
                    setShowBadge(false)
                },
            )
        }
    }

    private fun broadcast(action: String) {
        sendBroadcast(Intent(action).setPackage(packageName))
    }

    override fun onDestroy() {
        runCatching {
            player?.release()
            session?.release()
        }
        player = null
        session = null
        super.onDestroy()
    }

    companion object {
        const val ACTION_PLAY = "net.neciparmagan.trdriver.PLAY"
        const val ACTION_STOP = "net.neciparmagan.trdriver.STOP"
        const val ACTION_PAUSE = "net.neciparmagan.trdriver.PAUSE"
        const val ACTION_RESUME = "net.neciparmagan.trdriver.RESUME"
        const val ACTION_TOGGLE = "net.neciparmagan.trdriver.TOGGLE"
        const val ACTION_STATE = "net.neciparmagan.trdriver.MUSIC_STATE"
        const val EXTRA_URL = "url"
        const val EXTRA_TITLE = "title"
        const val EXTRA_TOKEN = "token"
        private const val NOTIF_ID = 42
        private const val CHANNEL_ID = "trdriver_music"

        @Volatile
        var currentTitle: String = ""

        @Volatile
        var currentUrl: String = ""

        @Volatile
        var currentToken: String = ""

        @Volatile
        var isPlaying: Boolean = false

        @Volatile
        var isSessionActive: Boolean = false
    }
}
