package net.neciparmagan.trdriver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.Button
import android.widget.SeekBar
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat

class PlayerActivity : AppCompatActivity() {
    private val handler = Handler(Looper.getMainLooper())
    private lateinit var titleView: TextView
    private lateinit var statusView: TextView
    private lateinit var seek: SeekBar
    private lateinit var posView: TextView
    private lateinit var durView: TextView
    private lateinit var toggle: Button

    private val tick = object : Runnable {
        override fun run() {
            // Progress is approximate until we bind; status text is enough for v1.
            handler.postDelayed(this, 1000)
        }
    }

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            statusView.text = "Bildirim / kilit ekranından da kontrol edebilirsiniz"
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_player)

        titleView = findViewById(R.id.playerTitle)
        statusView = findViewById(R.id.playerStatus)
        seek = findViewById(R.id.playerSeek)
        posView = findViewById(R.id.playerPosition)
        durView = findViewById(R.id.playerDuration)
        toggle = findViewById(R.id.btnPlayerToggle)

        val title = intent.getStringExtra(MusicService.EXTRA_TITLE).orEmpty().ifBlank { "Müzik" }
        val url = intent.getStringExtra(MusicService.EXTRA_URL).orEmpty()
        val token = intent.getStringExtra(MusicService.EXTRA_TOKEN).orEmpty()
        titleView.text = title
        statusView.text = "Çalınıyor · bildirimden kontrol edin"

        if (url.isNotBlank()) {
            val play = Intent(this, MusicService::class.java).apply {
                action = MusicService.ACTION_PLAY
                putExtra(MusicService.EXTRA_URL, url)
                putExtra(MusicService.EXTRA_TITLE, title)
                putExtra(MusicService.EXTRA_TOKEN, token)
            }
            ContextCompat.startForegroundService(this, play)
        }

        toggle.setOnClickListener {
            startService(Intent(this, MusicService::class.java).setAction(MusicService.ACTION_TOGGLE))
        }
        findViewById<Button>(R.id.btnPlayerStop).setOnClickListener {
            startService(Intent(this, MusicService::class.java).setAction(MusicService.ACTION_STOP))
            finish()
        }
        findViewById<Button>(R.id.btnPlayerClose).setOnClickListener { finish() }

        seek.isEnabled = false
    }

    override fun onStart() {
        super.onStart()
        val filter = IntentFilter(MusicService.ACTION_STATE)
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(receiver, filter, RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            registerReceiver(receiver, filter)
        }
        handler.post(tick)
    }

    override fun onStop() {
        handler.removeCallbacks(tick)
        runCatching { unregisterReceiver(receiver) }
        super.onStop()
    }

    companion object {
        fun start(context: Context, title: String, url: String, token: String) {
            context.startActivity(
                Intent(context, PlayerActivity::class.java).apply {
                    putExtra(MusicService.EXTRA_TITLE, title)
                    putExtra(MusicService.EXTRA_URL, url)
                    putExtra(MusicService.EXTRA_TOKEN, token)
                },
            )
        }
    }
}
