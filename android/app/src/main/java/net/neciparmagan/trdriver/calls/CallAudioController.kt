package net.neciparmagan.trdriver.calls

import android.content.Context
import android.content.Intent
import android.media.AudioAttributes
import android.media.AudioDeviceInfo
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.MediaPlayer
import android.media.RingtoneManager
import android.media.ToneGenerator
import android.os.Build
import android.os.PowerManager
import android.os.VibrationEffect
import android.os.Vibrator
import android.os.VibratorManager
import net.neciparmagan.trdriver.MusicService
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Owns audio focus, ringtone, ringback tone, earpiece/speaker/Bluetooth
 * routing, and proximity screen-off wake lock for active LocalTel calls.
 */
class CallAudioController(context: Context) {
    private val appContext = context.applicationContext
    private val audioManager = appContext.getSystemService(AudioManager::class.java)
    private val powerManager = appContext.getSystemService(PowerManager::class.java)
    private val scheduler = Executors.newSingleThreadScheduledExecutor()

    private var focusRequest: AudioFocusRequest? = null
    private var ringtonePlayer: MediaPlayer? = null
    private var ringbackGenerator: ToneGenerator? = null
    private var ringbackFuture: ScheduledFuture<*>? = null
    private var proximityLock: PowerManager.WakeLock? = null
    private var previousAudioMode: Int? = null
    private var focusHeld = false
    private var vibrator: Vibrator? = null

    private val focusListener = AudioManager.OnAudioFocusChangeListener {
        // Call retains communication mode until explicitly hung up or rejected.
    }

    @Synchronized
    fun startIncomingRinging() {
        pauseBackgroundMusicIfPlaying()
        requestAudioFocus(forRingtone = true)
        stopRingback()
        if (ringtonePlayer != null) return

        val mode = audioManager?.ringerMode ?: AudioManager.RINGER_MODE_NORMAL
        if (mode == AudioManager.RINGER_MODE_NORMAL) {
            val uri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE)
                ?: RingtoneManager.getDefaultUri(RingtoneManager.TYPE_NOTIFICATION)
            if (uri != null) {
                runCatching {
                    val mp = MediaPlayer()
                    mp.setAudioAttributes(
                        AudioAttributes.Builder()
                            .setUsage(AudioAttributes.USAGE_NOTIFICATION_RINGTONE)
                            .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                            .build(),
                    )
                    mp.setDataSource(appContext, uri)
                    mp.isLooping = true
                    mp.prepare()
                    mp.start()
                    ringtonePlayer = mp
                }.onFailure {
                    ringtonePlayer = null
                }
            }
        }
        if (mode != AudioManager.RINGER_MODE_SILENT) {
            startVibration()
        }
    }

    @Synchronized
    fun startOutgoingRingback() {
        pauseBackgroundMusicIfPlaying()
        stopRinging()
        enterInCommunicationAudio(speakerOn = false)
        if (ringbackFuture != null) return
        runCatching {
            val tg = ToneGenerator(AudioManager.STREAM_VOICE_CALL, 50)
            ringbackGenerator = tg
            ringbackFuture = scheduler.scheduleAtFixedRate(
                {
                    runCatching { tg.startTone(ToneGenerator.TONE_SUP_RINGTONE, 1000) }
                },
                0L,
                4000L,
                TimeUnit.MILLISECONDS,
            )
        }
    }

    @Synchronized
    fun enterInCommunicationAudio(speakerOn: Boolean) {
        pauseBackgroundMusicIfPlaying()
        stopRinging()
        requestAudioFocus(forRingtone = false)
        audioManager?.let { am ->
            if (previousAudioMode == null) {
                previousAudioMode = am.mode
            }
            runCatching { am.mode = AudioManager.MODE_IN_COMMUNICATION }
        }
        setSpeakerphone(speakerOn)
    }

    @Synchronized
    fun setSpeakerphone(speakerOn: Boolean) {
        val am = audioManager ?: return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            val targetType = if (speakerOn) {
                AudioDeviceInfo.TYPE_BUILTIN_SPEAKER
            } else {
                AudioDeviceInfo.TYPE_BUILTIN_EARPIECE
            }
            val devices = am.availableCommunicationDevices
            val bt = if (!speakerOn) {
                devices.firstOrNull {
                    it.type == AudioDeviceInfo.TYPE_BLUETOOTH_SCO ||
                        it.type == AudioDeviceInfo.TYPE_BLE_HEADSET ||
                        it.type == AudioDeviceInfo.TYPE_WIRED_HEADSET ||
                        it.type == AudioDeviceInfo.TYPE_WIRED_HEADPHONES ||
                        it.type == AudioDeviceInfo.TYPE_USB_HEADSET
                }
            } else {
                null
            }
            val chosen = bt ?: devices.firstOrNull { it.type == targetType }
            if (chosen != null) {
                runCatching { am.setCommunicationDevice(chosen) }
            } else if (!speakerOn) {
                runCatching { am.clearCommunicationDevice() }
            }
        } else {
            @Suppress("DEPRECATION")
            runCatching { am.isSpeakerphoneOn = speakerOn }
        }
    }

    @Synchronized
    fun setMicrophoneMute(muted: Boolean) {
        runCatching { audioManager?.isMicrophoneMute = muted }
    }

    @Synchronized
    fun updateProximityLock(acquire: Boolean) {
        if (acquire) {
            if (proximityLock?.isHeld == true) return
            val pm = powerManager ?: return
            if (!pm.isWakeLockLevelSupported(PowerManager.PROXIMITY_SCREEN_OFF_WAKE_LOCK)) return
            runCatching {
                proximityLock = pm.newWakeLock(
                    PowerManager.PROXIMITY_SCREEN_OFF_WAKE_LOCK,
                    "TRDriver:CallProximity",
                ).apply {
                    setReferenceCounted(false)
                    acquire(60L * 60L * 1000L)
                }
            }
        } else {
            releaseProximityLock()
        }
    }

    @Synchronized
    fun stopRinging() {
        ringtonePlayer?.let { mp ->
            runCatching {
                if (mp.isPlaying) mp.stop()
            }
            runCatching { mp.release() }
        }
        ringtonePlayer = null
        stopVibration()
    }

    @Synchronized
    fun stopRingback() {
        ringbackFuture?.cancel(true)
        ringbackFuture = null
        runCatching { ringbackGenerator?.stopTone() }
        runCatching { ringbackGenerator?.release() }
        ringbackGenerator = null
    }

    @Synchronized
    fun releaseAll() {
        stopRinging()
        stopRingback()
        releaseProximityLock()
        setMicrophoneMute(false)
        audioManager?.let { am ->
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                runCatching { am.clearCommunicationDevice() }
            } else {
                @Suppress("DEPRECATION")
                runCatching { am.isSpeakerphoneOn = false }
            }
            previousAudioMode?.let { mode ->
                runCatching { am.mode = mode }
            }
            previousAudioMode = null
            if (focusHeld) {
                focusRequest?.let { req -> runCatching { am.abandonAudioFocusRequest(req) } }
                focusHeld = false
            }
        }
    }

    private fun pauseBackgroundMusicIfPlaying() {
        if (!MusicService.isPlaying) return
        runCatching {
            appContext.startService(
                Intent(appContext, MusicService::class.java).setAction(MusicService.ACTION_PAUSE),
            )
        }
    }

    private fun requestAudioFocus(forRingtone: Boolean) {
        val am = audioManager ?: return
        focusRequest?.let { prev ->
            if (focusHeld) runCatching { am.abandonAudioFocusRequest(prev) }
        }
        val usage = if (forRingtone) {
            AudioAttributes.USAGE_NOTIFICATION_RINGTONE
        } else {
            AudioAttributes.USAGE_VOICE_COMMUNICATION
        }
        val content = if (forRingtone) {
            AudioAttributes.CONTENT_TYPE_SONIFICATION
        } else {
            AudioAttributes.CONTENT_TYPE_SPEECH
        }
        val req = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(usage)
                    .setContentType(content)
                    .build(),
            )
            .setAcceptsDelayedFocusGain(false)
            .setOnAudioFocusChangeListener(focusListener)
            .build()
        focusRequest = req
        am.requestAudioFocus(req)
        focusHeld = true
    }

    private fun startVibration() {
        val vib = resolveVibrator() ?: return
        vibrator = vib
        if (!vib.hasVibrator()) return
        val pattern = longArrayOf(0L, 600L, 1000L)
        runCatching {
            vib.vibrate(VibrationEffect.createWaveform(pattern, 0))
        }
    }

    private fun stopVibration() {
        runCatching { vibrator?.cancel() }
        vibrator = null
    }

    private fun resolveVibrator(): Vibrator? {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            appContext.getSystemService(VibratorManager::class.java)?.defaultVibrator
        } else {
            @Suppress("DEPRECATION")
            appContext.getSystemService(Vibrator::class.java)
        }
    }

    private fun releaseProximityLock() {
        proximityLock?.let { wl ->
            runCatching {
                if (wl.isHeld) wl.release(PowerManager.RELEASE_FLAG_WAIT_FOR_NO_PROXIMITY)
            }.onFailure {
                runCatching { if (wl.isHeld) wl.release() }
            }
        }
        proximityLock = null
    }
}
