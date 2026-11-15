package net.neciparmagan.trdriver.calls

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.IBinder
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.SessionStore

/**
 * Maintains LocalTel registration so incoming IP calls can wake the device
 * over either Wi-Fi or mobile data, completely independent from the Wi-Fi-only
 * Drive backup service.
 */
class LocalTelListenerService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var statusJob: Job? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        val notif = CallNotifications.buildListenerNotification(
            this,
            CallController.providerStatus.value,
        )
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            runCatching {
                ServiceCompat.startForeground(
                    this,
                    CallNotifications.LISTENER_NOTIFICATION_ID,
                    notif,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
                )
            }.onFailure {
                startForeground(CallNotifications.LISTENER_NOTIFICATION_ID, notif)
            }
        } else {
            startForeground(CallNotifications.LISTENER_NOTIFICATION_ID, notif)
        }

        registerNetworkObserver()
        statusJob = scope.launch {
            CallController.providerStatus.collectLatest { snap ->
                val updated = CallNotifications.buildListenerNotification(this@LocalTelListenerService, snap)
                val nm = getSystemService(android.app.NotificationManager::class.java)
                nm?.notify(CallNotifications.LISTENER_NOTIFICATION_ID, updated)
            }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            CallController.disconnectProvider()
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }

        val session = SessionStore(applicationContext)
        val cfg = session.loadLocalTelConfig()
        if (!cfg.isReady(session.serverUrl) || !cfg.autoRegister) {
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }
        CallController.ensureProviderStarted(applicationContext)
        return START_STICKY
    }

    override fun onDestroy() {
        unregisterNetworkObserver()
        statusJob?.cancel()
        scope.cancel()
        super.onDestroy()
    }

    private fun registerNetworkObserver() {
        val cm = getSystemService(ConnectivityManager::class.java) ?: return
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                CallController.ensureProviderStarted(applicationContext, forceRestart = true)
            }
        }
        networkCallback = cb
        runCatching { cm.registerNetworkCallback(req, cb) }
    }

    private fun unregisterNetworkObserver() {
        val cm = getSystemService(ConnectivityManager::class.java) ?: return
        networkCallback?.let { runCatching { cm.unregisterNetworkCallback(it) } }
        networkCallback = null
    }

    companion object {
        const val ACTION_START = "net.neciparmagan.trdriver.calls.LISTENER_START"
        const val ACTION_STOP = "net.neciparmagan.trdriver.calls.LISTENER_STOP"

        fun syncState(context: Context) {
            val session = SessionStore(context)
            val cfg = session.loadLocalTelConfig()
            if (cfg.isReady(session.serverUrl) && cfg.autoRegister) {
                val intent = Intent(context, LocalTelListenerService::class.java).setAction(ACTION_START)
                runCatching { ContextCompat.startForegroundService(context, intent) }
            } else {
                stop(context)
            }
        }

        fun stop(context: Context) {
            runCatching {
                context.startService(
                    Intent(context, LocalTelListenerService::class.java).setAction(ACTION_STOP),
                )
            }
        }
    }
}
