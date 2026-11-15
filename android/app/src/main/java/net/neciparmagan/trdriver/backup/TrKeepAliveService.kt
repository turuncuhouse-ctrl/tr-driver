package net.neciparmagan.trdriver.backup

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.database.ContentObserver
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.provider.ContactsContract
import android.provider.MediaStore
import android.provider.Telephony
import android.util.Log
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.CommsAccess
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.widget.BackupStatusWidget

/**
 * Persistent, user-visible foreground service that OWNS Wi-Fi gallery backup.
 *
 * This is the only reliable way on Android 12+ / HyperOS to keep a backup loop
 * alive while the app is in the background. It holds:
 *  - a ConnectivityManager callback (survives as long as the process),
 *  - a MediaStore ContentObserver (new photo/video => immediate backup),
 *  - a conflated wake channel fed by every BackupController trigger,
 *  - a 15-minute watchdog tick so a stalled loop always re-arms itself.
 *
 * Idle state does NOT poll: the loop suspends on the wake channel. A partial
 * wake lock is held only while a batch is uploading.
 */
class TrKeepAliveService : Service() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val mainHandler = Handler(Looper.getMainLooper())
    private var loop: Job? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private var mediaObserver: ContentObserver? = null
    private var commsObserver: ContentObserver? = null
    private val commsObservedUris = mutableSetOf<android.net.Uri>()
    private var wakeLock: PowerManager.WakeLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            loop?.cancel()
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }

        BackupNotifications.ensureChannel(this)
        val started = try {
            val n = BackupNotifications.build(
                this,
                "TR Driver yedek",
                "Arka planda hazır · yalnız Wi‑Fi",
            )
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                startForeground(
                    BackupNotifications.KEEPALIVE_NOTIFICATION_ID,
                    n,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
                )
            } else {
                startForeground(BackupNotifications.KEEPALIVE_NOTIFICATION_ID, n)
            }
            true
        } catch (e: Exception) {
            Log.w(TAG, "startForeground failed: ${e.message}")
            false
        }
        if (!started) {
            // Android 12+ background FGS restriction: let WorkManager handle the batch.
            BackupController.scheduleContinue(this, 15L)
            stopSelf()
            return START_NOT_STICKY
        }

        running = true
        registerNetworkCallback()
        registerMediaObserver()
        registerCommsObserver()
        if (loop?.isActive != true) {
            loop = scope.launch {
                supervise(isStopped = { !isActive })
            }
        }
        BackupController.requestKick()
        return START_STICKY
    }

    private suspend fun supervise(isStopped: () -> Boolean) {
        val app = applicationContext
        var cycles = 0
        while (!isStopped()) {
            val session = SessionStore(app)
            if (!session.isLoggedIn || !session.anyBackupEnabled()) {
                Log.i(TAG, "backup disabled — stopping keep-alive")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return
            }

            checkStalled(app, session)
            registerCommsObserver()

            if (!UploadNetworkGate.isWifi(app)) {
                notify("Wi‑Fi bekleniyor · yedek hazır")
                // Wake immediately when Wi-Fi returns, or after the watchdog timeout.
                BackupController.awaitWake(WATCHDOG_MS)
                continue
            }

            notify("Yedek denetleniyor…")
            var batches = 0
            while (!isStopped() && batches < MAX_BATCHES_PER_CYCLE) {
                batches++
                val result = try {
                    acquireWakeLock()
                    GalleryBackupEngine.runBatch(
                        context = app,
                        isStopped = isStopped,
                        onNotification = { text -> notify(text) },
                    )
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Log.w(TAG, "batch failed: ${e.message}")
                    break
                } finally {
                    releaseWakeLock()
                }
                session.lastBackupHeartbeatAt = System.currentTimeMillis()
                if (!result.scheduleContinue || !result.morePending) break
                delay(
                    (result.continueDelaySec.coerceIn(2L, 60L) * 1000L).coerceAtLeast(2_000L),
                )
            }

            // Contacts / SMS ride the same Wi-Fi window and the same service,
            // right after the media batches, so one foreground session covers
            // every kind of backup the user enabled.
            var commsPending = false
            if (session.anyCommsBackupEnabled()) {
                val comms = try {
                    PersonalBackupEngine.runBatch(app, isStopped)
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Log.w(TAG, "comms backup failed: ${e.message}")
                    null
                }
                if (comms != null) {
                    session.lastBackupHeartbeatAt = System.currentTimeMillis()
                    commsPending = comms.morePending
                    if (comms.sentItems > 0 || comms.deletedItems > 0) {
                        notify(comms.message.ifBlank { "Rehber/SMS yedeği güncel" })
                    }
                }
            }

            cycles++
            if (cycles % VERIFY_EVERY_CYCLES == 0) {
                BackupVerifier.verifyIfDue(app)
            }
            BackupStatusWidget.refreshAll(app)
            notify("Yedek güncel · Wi‑Fi izleniyor")
            if (commsPending) {
                // A large address book or a slow link: come back shortly
                // instead of holding the CPU for one huge run.
                BackupController.awaitWake(CONTINUE_SOON_MS)
            }
            // Idle: new media, Wi-Fi change, manual kick, or watchdog. No busy polling.
            BackupController.awaitWake(WATCHDOG_MS)
            BackupController.ensure(app) // re-arm WorkManager after every cycle
        }
    }

    private fun checkStalled(context: Context, session: SessionStore) {
        val last = session.lastBackupHeartbeatAt
        if (last <= 0L) return
        val stalled = System.currentTimeMillis() - last > STALL_AFTER_MS
        if (stalled && session.backupPendingCount > 0 && session.galleryBackupEnabled) {
            BackupNotifications.notifyStalled(context)
        }
    }

    private fun notify(text: String) {
        runCatching {
            val nm = getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager
            nm.notify(
                BackupNotifications.KEEPALIVE_NOTIFICATION_ID,
                BackupNotifications.build(this, "TR Driver yedek", text),
            )
        }
    }

    private fun registerNetworkCallback() {
        if (networkCallback != null) return
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager ?: return
        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                BackupController.requestKick()
            }

            override fun onLost(network: Network) {
                BackupController.requestKick()
            }

            override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
                    caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) ||
                    caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)
                ) {
                    BackupController.requestKick()
                }
            }
        }
        try {
            cm.registerNetworkCallback(request, callback, mainHandler)
            networkCallback = callback
        } catch (e: Exception) {
            Log.w(TAG, "registerNetworkCallback failed", e)
        }
    }

    private fun unregisterNetworkCallback() {
        val cb = networkCallback ?: return
        networkCallback = null
        runCatching {
            val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            cm?.unregisterNetworkCallback(cb)
        }
    }

    /**
     * New / edited contact or new SMS => wake the loop. Registering an observer
     * on a provider we cannot read throws SecurityException, so each is guarded
     * by the matching runtime permission.
     */
    private fun registerCommsObserver() {
        val targets = buildList {
            if (CommsAccess.hasContactsAccess(this@TrKeepAliveService)) {
                add(ContactsContract.Contacts.CONTENT_URI)
            }
            if (CommsAccess.hasSmsAccess(this@TrKeepAliveService)) {
                add(Telephony.Sms.CONTENT_URI)
            }
        }
        if (targets.isEmpty()) return
        synchronized(commsObservedUris) {
            val missing = targets.filterNot { it in commsObservedUris }
            if (missing.isEmpty()) return
            val observer = commsObserver ?: object : ContentObserver(mainHandler) {
                override fun onChange(selfChange: Boolean) {
                    BackupController.requestKick()
                }
            }.also { commsObserver = it }
            try {
                missing.forEach { uri ->
                    contentResolver.registerContentObserver(uri, true, observer)
                    commsObservedUris.add(uri)
                }
            } catch (e: Exception) {
                Log.w(TAG, "registerCommsObserver failed: ${e.message}")
            }
        }
    }

    private fun unregisterCommsObserver() {
        val obs = commsObserver
        commsObserver = null
        synchronized(commsObservedUris) { commsObservedUris.clear() }
        if (obs == null) return
        runCatching { contentResolver.unregisterContentObserver(obs) }
    }

    /** New photo / screenshot / video => back up immediately, like Google Photos. */
    private fun registerMediaObserver() {
        if (mediaObserver != null) return
        val observer = object : ContentObserver(mainHandler) {
            override fun onChange(selfChange: Boolean) {
                BackupController.requestKick()
            }
        }
        try {
            contentResolver.registerContentObserver(
                MediaStore.Images.Media.EXTERNAL_CONTENT_URI, true, observer,
            )
            contentResolver.registerContentObserver(
                MediaStore.Video.Media.EXTERNAL_CONTENT_URI, true, observer,
            )
            mediaObserver = observer
        } catch (e: Exception) {
            Log.w(TAG, "registerContentObserver failed", e)
        }
    }

    private fun unregisterMediaObserver() {
        val obs = mediaObserver ?: return
        mediaObserver = null
        runCatching { contentResolver.unregisterContentObserver(obs) }
    }

    private fun acquireWakeLock() {
        try {
            val pm = getSystemService(Context.POWER_SERVICE) as? PowerManager ?: return
            releaseWakeLock()
            val lock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, WAKE_LOCK_TAG)
            lock.setReferenceCounted(false)
            lock.acquire(WAKE_LOCK_TIMEOUT_MS)
            wakeLock = lock
        } catch (e: Exception) {
            Log.w(TAG, "wake lock unavailable: ${e.message}")
        }
    }

    private fun releaseWakeLock() {
        runCatching { if (wakeLock?.isHeld == true) wakeLock?.release() }
        wakeLock = null
    }

    override fun onDestroy() {
        loop?.cancel()
        scope.cancel()
        unregisterNetworkCallback()
        unregisterMediaObserver()
        unregisterCommsObserver()
        releaseWakeLock()
        running = false
        super.onDestroy()
    }

    companion object {
        private const val TAG = "TrKeepAlive"
        private const val ACTION_STOP = "trdriver.backup.stop"
        private const val WAKE_LOCK_TAG = "trdriver:gallery-backup"
        private const val WAKE_LOCK_TIMEOUT_MS = 10L * 60L * 1000L
        private const val WATCHDOG_MS = 15L * 60L * 1000L
        private const val CONTINUE_SOON_MS = 5L * 1000L
        private const val STALL_AFTER_MS = 24L * 60L * 60L * 1000L
        private const val MAX_BATCHES_PER_CYCLE = 250
        private const val VERIFY_EVERY_CYCLES = 4

        @Volatile
        var running: Boolean = false
            private set

        fun startIfNeeded(context: Context) {
            val session = SessionStore(context)
            if (!session.isLoggedIn || !session.anyBackupEnabled()) {
                stop(context)
                return
            }
            val app = context.applicationContext
            try {
                val intent = Intent(app, TrKeepAliveService::class.java)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    app.startForegroundService(intent)
                } else {
                    app.startService(intent)
                }
            } catch (e: Exception) {
                // Android 12+ refuses background FGS starts. WorkManager fallback
                // still runs, and the next user-visible launch re-arms the service.
                Log.w(TAG, "keep-alive start refused, using WorkManager fallback", e)
                BackupController.scheduleContinue(app, 15L)
            }
        }

        fun stop(context: Context) {
            runCatching {
                val intent = Intent(context.applicationContext, TrKeepAliveService::class.java)
                intent.action = ACTION_STOP
                context.applicationContext.stopService(intent)
            }
        }
    }
}
