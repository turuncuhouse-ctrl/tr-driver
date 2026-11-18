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
import android.os.Handler
import android.os.IBinder
import android.os.Looper
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
    private var tokenRefreshJob: Job? = null
    private var gatewayRetryJob: Job? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private val knownNetworks = java.util.concurrent.ConcurrentHashMap<Network, NetworkCapabilities>()
    private var chosenNetworkId: String? = null
    private val mainHandler = Handler(Looper.getMainLooper())

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
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE,
                )
            }.onFailure {
                startForeground(CallNotifications.LISTENER_NOTIFICATION_ID, notif)
            }
        } else {
            startForeground(CallNotifications.LISTENER_NOTIFICATION_ID, notif)
        }

        acquireLocks()
        registerNetworkObserver()
        statusJob = scope.launch {
            CallController.providerStatus.collectLatest { snap ->
                val updated = CallNotifications.buildListenerNotification(this@LocalTelListenerService, snap)
                val nm = getSystemService(android.app.NotificationManager::class.java)
                runCatching { nm?.notify(CallNotifications.LISTENER_NOTIFICATION_ID, updated) }
                if (snap.state == ProviderRegistrationState.REGISTERED) {
                    gatewayRetryJob?.cancel()
                    gatewayRetryJob = null
                } else if (snap.state == ProviderRegistrationState.ERROR && gatewayRetryJob?.isActive != true) {
                    gatewayRetryJob = launch {
                        var delayMs = 3_000L
                        while (true) {
                            kotlinx.coroutines.delay(delayMs)
                            val store = SessionStore(applicationContext)
                            if (!store.isLoggedIn || !store.localTelEnabled) break
                            val refreshed = LocalTelConfigApi(store).fetchGatewayConfig()
                            if (refreshed.isSuccess) {
                                refreshed.getOrThrow().also(store::saveLocalTelConfig)
                            }
                            CallController.ensureProviderStarted(applicationContext, forceRestart = true)
                            delayMs = (delayMs * 2).coerceAtMost(60_000L)
                            if (CallController.providerStatus.value.state == ProviderRegistrationState.REGISTERED) break
                        }
                        gatewayRetryJob = null
                    }
                }
            }
        }
        tokenRefreshJob = scope.launch {
            val store = SessionStore(applicationContext)
            while (true) {
                kotlinx.coroutines.delay(5 * 60_000L)
                if (!store.isLoggedIn || !store.localTelEnabled ||
                    store.localTelProviderType != LocalTelProviderType.GATEWAY.wireValue
                ) continue
                LocalTelConfigApi(store).fetchGatewayConfig().onSuccess { refreshed ->
                    // Only keep the fresh short-lived token for the next
                    // reconnect. Restarting a healthy socket here made the line
                    // drop out and miss incoming calls.
                    store.saveLocalTelConfig(refreshed)
                    if (CallController.providerStatus.value.state != ProviderRegistrationState.REGISTERED &&
                        !CallController.currentState().isLive
                    ) {
                        CallController.ensureProviderStarted(applicationContext, forceRestart = true)
                    }
                }
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
        releaseLocks()
        unregisterNetworkObserver()
        statusJob?.cancel()
        tokenRefreshJob?.cancel()
        gatewayRetryJob?.cancel()
        scope.cancel()
        super.onDestroy()
    }

    private var cpuLock: android.os.PowerManager.WakeLock? = null
    private var wifiLock: android.net.wifi.WifiManager.WifiLock? = null

    /** Keeps the signaling socket alive with the screen off (idle Wi-Fi/CPU). */
    @android.annotation.SuppressLint("WakelockTimeout")
    private fun acquireLocks() {
        runCatching {
            val pm = getSystemService(android.os.PowerManager::class.java)
            cpuLock = pm?.newWakeLock(android.os.PowerManager.PARTIAL_WAKE_LOCK, "trdriver:localtel")
                ?.apply { setReferenceCounted(false); acquire() }
        }
        runCatching {
            val wm = applicationContext.getSystemService(android.net.wifi.WifiManager::class.java)
            @Suppress("DEPRECATION")
            wifiLock = wm?.createWifiLock(android.net.wifi.WifiManager.WIFI_MODE_FULL_HIGH_PERF, "trdriver:localtel")
                ?.apply { setReferenceCounted(false); acquire() }
        }
    }

    private fun releaseLocks() {
        runCatching { cpuLock?.takeIf { it.isHeld }?.release() }
        runCatching { wifiLock?.takeIf { it.isHeld }?.release() }
        cpuLock = null
        wifiLock = null
    }

    private fun registerNetworkObserver() {
        val cm = getSystemService(ConnectivityManager::class.java) ?: return
        // A process-wide bind (used by older backup builds) would pin calls to Wi-Fi.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && !CallNetworkPolicy.shouldBindProcessToNetwork()) {
            runCatching { cm.bindProcessToNetwork(null) }
        }
        // Any internet transport: Wi-Fi, Ethernet and mobile data. Not NOT_METERED.
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                cm.getNetworkCapabilities(network)?.let { knownNetworks[network] = it }
                reselectCallNetwork(cm)
            }

            override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                knownNetworks[network] = caps
                reselectCallNetwork(cm)
            }

            override fun onLost(network: Network) {
                knownNetworks.remove(network)
                reselectCallNetwork(cm)
            }
        }
        networkCallback = cb
        runCatching { cm.registerNetworkCallback(req, cb) }
    }

    /**
     * Pick Wi-Fi or mobile data for the signaling socket and reopen it quickly
     * when that choice changes. Backup keeps its own unmetered gate.
     */
    private fun reselectCallNetwork(cm: ConnectivityManager) {
        val pairs = knownNetworks.map { (network, caps) ->
            network to CallNetworkPolicy.NetChoice(
                id = network.toString(),
                validated = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED),
                hasInternet = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET),
                isDefault = network == cm.activeNetwork,
                wifi = caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI),
                cellular = caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR),
                ethernet = caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET),
                vpn = caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN),
            )
        }
        val best = CallNetworkPolicy.choose(pairs.map { it.second })
        val network = best?.let { choice -> pairs.firstOrNull { it.second.id == choice.id }?.first }
        CallNetwork.update(network)
        if (CallNetworkPolicy.handover(chosenNetworkId, best)) {
            chosenNetworkId = best?.id
            mainHandler.postDelayed({
                CallController.ensureProviderStarted(applicationContext, forceRestart = true)
            }, CallNetworkPolicy.HANDOVER_RECONNECT_MS)
        } else {
            chosenNetworkId = best?.id
        }
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
