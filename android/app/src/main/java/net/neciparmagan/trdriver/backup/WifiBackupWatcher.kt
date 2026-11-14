package net.neciparmagan.trdriver.backup

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Handler
import android.os.Looper
import android.util.Log

/**
 * In-process Wi-Fi trigger.
 *
 * A dynamic NetworkCallback only lives as long as the process. The durable
 * network callback lives in TrKeepAliveService; this one is the extra trigger
 * while the app process happens to be alive. It must NOT start a foreground
 * service (Android 12+ background FGS restriction) — it only re-arms
 * WorkManager and nudges the service loop.
 */
object WifiBackupWatcher {
    private const val TAG = "WifiBackupWatcher"

    @Volatile
    private var registered = false
    private val mainHandler = Handler(Looper.getMainLooper())
    private var pendingStart: Runnable? = null

    fun start(context: Context) {
        if (registered) return
        val app = context.applicationContext
        val cm = app.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return
        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                scheduleBackupSoon(app)
            }

            override fun onLost(network: Network) {
                scheduleBackupSoon(app)
            }

            override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
                    caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) ||
                    caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)
                ) {
                    scheduleBackupSoon(app)
                }
            }
        }
        try {
            cm.registerNetworkCallback(request, callback, mainHandler)
            registered = true
            scheduleBackupSoon(app)
        } catch (e: Exception) {
            Log.w(TAG, "registerNetworkCallback failed", e)
        }
    }

    private fun scheduleBackupSoon(context: Context) {
        pendingStart?.let { mainHandler.removeCallbacks(it) }
        val task = Runnable {
            try {
                // Debounce: let Wi-Fi stabilise and let other apps get airtime.
                BackupController.ensure(context)
                BackupController.requestKick()
            } catch (e: Exception) {
                Log.e(TAG, "scheduleBackupSoon failed", e)
            }
        }
        pendingStart = task
        mainHandler.postDelayed(task, 4_000L)
    }
}
