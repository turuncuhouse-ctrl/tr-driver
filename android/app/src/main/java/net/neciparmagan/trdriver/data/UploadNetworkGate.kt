package net.neciparmagan.trdriver.data

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.SystemClock
import androidx.work.NetworkType
import kotlinx.coroutines.delay
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume

/**
 * Network policy:
 * - App browse / download / manual upload: any internet (Wi‑Fi or mobile).
 * - Automatic gallery backup: Wi‑Fi / Ethernet only.
 */
object UploadNetworkGate {
    private const val VALIDATED_FALLBACK_MS = 18_000L

    fun isWifi(context: Context): Boolean {
        val caps = activeCaps(context) ?: return false
        return caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)
    }

    fun isCellular(context: Context): Boolean {
        val caps = activeCaps(context) ?: return false
        return caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)
    }

    fun hasInternet(context: Context): Boolean {
        val caps = activeCaps(context) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
    }

    fun hasValidatedInternet(context: Context): Boolean {
        val caps = activeCaps(context) ?: return false
        if (!caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) return false
        return Build.VERSION.SDK_INT < Build.VERSION_CODES.M ||
            caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
    }

    /**
     * @param wifiOnly true for automatic backup; false for normal app usage uploads.
     */
    fun allowsUploadNow(
        context: Context,
        session: SessionStore,
        fileBytes: Long,
        wifiOnly: Boolean = false,
    ): Boolean {
        if (!hasInternet(context)) return false
        return if (wifiOnly) isWifi(context) else true
    }

    suspend fun awaitUploadAllowed(
        context: Context,
        session: SessionStore,
        fileBytes: Long,
        timeoutMs: Long = 120_000L,
        wifiOnly: Boolean = false,
    ) {
        if (allowsUploadNow(context, session, fileBytes, wifiOnly)) return
        val reason = blockReason(context, session, fileBytes, wifiOnly)
        if (!wifiOnly) {
            // Mobile / any network: just wait briefly for general internet
            val started = SystemClock.elapsedRealtime()
            withTimeoutOrNull(timeoutMs) {
                while (!hasInternet(context)) {
                    delay(500)
                    if (SystemClock.elapsedRealtime() - started > timeoutMs) break
                }
            } ?: throw UploadNetworkBlockedException(reason)
            if (!hasInternet(context)) throw UploadNetworkBlockedException(reason)
            return
        }
        val started = SystemClock.elapsedRealtime()
        withTimeoutOrNull(timeoutMs) {
            while (!allowsUploadNow(context, session, fileBytes, wifiOnly = true)) {
                awaitWifiNetwork(context, started)
                delay(500)
            }
        } ?: throw UploadNetworkBlockedException(reason)
    }

    fun blockReason(
        context: Context,
        session: SessionStore,
        fileBytes: Long,
        wifiOnly: Boolean = false,
    ): String {
        if (!hasInternet(context)) return "İnternet bağlantısı yok"
        if (wifiOnly) {
            if (isCellular(context) && !isWifi(context)) {
                return "Yedekleme için Wi‑Fi gerekli (mobil veri ile yedek kapalı)"
            }
            if (!isWifi(context)) return "Yedekleme için Wi‑Fi bekleniyor"
        }
        return "Ağ uygun değil"
    }

    /** WorkManager constraints for backup jobs only. */
    fun workManagerNetworkType(session: SessionStore): NetworkType = NetworkType.UNMETERED

    fun networkPolicyLabel(session: SessionStore): String =
        "yedek: yalnız Wi‑Fi · uygulama: mobil veri OK"

    /** Bind process to Wi‑Fi only during backup uploads (optional). */
    fun bindUploadNetwork(context: Context, wifiOnly: Boolean = true): Network? {
        if (!wifiOnly) return null
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return null
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return null
        val network = cm.activeNetwork ?: return null
        val caps = cm.getNetworkCapabilities(network) ?: return null
        val wifi = caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)
        if (!wifi) return null
        runCatching { cm.bindProcessToNetwork(network) }
        return network
    }

    fun unbindUploadNetwork(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return
        runCatching { cm.bindProcessToNetwork(null) }
    }

    private fun hasUsableInternet(context: Context, waitStartedMs: Long): Boolean {
        if (hasValidatedInternet(context)) return true
        if (!hasInternet(context)) return false
        return SystemClock.elapsedRealtime() - waitStartedMs >= VALIDATED_FALLBACK_MS
    }

    private fun activeCaps(context: Context): NetworkCapabilities? {
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return null
        val network = cm.activeNetwork ?: return null
        return cm.getNetworkCapabilities(network)
    }

    private suspend fun awaitWifiNetwork(context: Context, startedMs: Long) {
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return
        if (isWifi(context) && hasUsableInternet(context, startedMs)) return
        suspendCancellableCoroutine { cont ->
            val done = AtomicBoolean(false)
            val request = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                .addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
                .build()
            val callback = object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) {
                    tryResume(context, startedMs, cm, this, done, cont)
                }

                override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                    tryResume(context, startedMs, cm, this, done, cont)
                }
            }
            try {
                cm.registerNetworkCallback(request, callback)
            } catch (_: Exception) {
                if (done.compareAndSet(false, true)) cont.resume(Unit)
                return@suspendCancellableCoroutine
            }
            cont.invokeOnCancellation {
                runCatching { cm.unregisterNetworkCallback(callback) }
            }
            tryResume(context, startedMs, cm, callback, done, cont)
        }
    }

    private fun tryResume(
        context: Context,
        startedMs: Long,
        cm: ConnectivityManager,
        callback: ConnectivityManager.NetworkCallback,
        done: AtomicBoolean,
        cont: kotlinx.coroutines.CancellableContinuation<Unit>,
    ) {
        if (isWifi(context) && hasUsableInternet(context, startedMs) &&
            done.compareAndSet(false, true)
        ) {
            runCatching { cm.unregisterNetworkCallback(callback) }
            cont.resume(Unit)
        }
    }
}
