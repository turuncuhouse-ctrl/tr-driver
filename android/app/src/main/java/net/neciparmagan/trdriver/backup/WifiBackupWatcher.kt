package net.neciparmagan.trdriver.backup

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Handler
import android.os.Looper
import android.util.Log
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate

/**
 * When Wi‑Fi becomes available, resume gallery backup in the background
 * (throttled so other apps keep working).
 */
object WifiBackupWatcher {
    private const val TAG = "WifiBackupWatcher"
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
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
            .build()
        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                scheduleBackupSoon(app)
            }

            override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
                    caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)
                ) {
                    scheduleBackupSoon(app)
                }
            }
        }
        try {
            cm.registerNetworkCallback(request, callback)
            registered = true
            if (UploadNetworkGate.isWifi(app)) {
                scheduleBackupSoon(app)
            }
        } catch (e: Exception) {
            Log.w(TAG, "registerNetworkCallback failed", e)
        }
    }

    private fun scheduleBackupSoon(context: Context) {
        pendingStart?.let { mainHandler.removeCallbacks(it) }
        val task = Runnable {
            val session = SessionStore(context)
            if (!session.isLoggedIn || !session.anyBackupEnabled()) return@Runnable
            if (!UploadNetworkGate.isWifi(context)) return@Runnable
            if (GalleryBackupForegroundService.running) return@Runnable
            Log.i(TAG, "Wi‑Fi up — scheduling gallery backup")
            GalleryBackupWorker.schedule(context)
            GalleryBackupWorker.scheduleContinue(context, delaySeconds = 3)
        }
        pendingStart = task
        // Debounce: wait a few seconds for Wi‑Fi to stabilize.
        mainHandler.postDelayed(task, 4_000L)
    }
}
