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

/** Unified Wi‑Fi / mobile policy for gallery backup, intake, and manual uploads. */
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

    fun allowsUploadNow(context: Context, session: SessionStore, fileBytes: Long): Boolean {
        if (!hasInternet(context)) return false
        return networkAllowed(context, session)
    }

    suspend fun awaitUploadAllowed(
        context: Context,
        session: SessionStore,
        fileBytes: Long,
        timeoutMs: Long = 120_000L,
    ) {
        if (allowsUploadNow(context, session, fileBytes)) return
        val reason = blockReason(context, session, fileBytes)
        val started = SystemClock.elapsedRealtime()
        withTimeoutOrNull(timeoutMs) {
            while (!allowsUploadNow(context, session, fileBytes)) {
                awaitMatchingNetwork(context, session, started)
                delay(500)
            }
        } ?: throw UploadNetworkBlockedException(reason)
    }

    fun blockReason(context: Context, session: SessionStore, fileBytes: Long): String {
        if (!hasInternet(context)) return "İnternet bağlantısı yok"
        if (!session.backupOnWifi && !session.backupOnMobile) {
            return "Yedekleme ağı kapalı (Wi‑Fi ve mobil kapalı)"
        }
        if (isWifi(context) && !session.backupOnWifi) {
            return "Wi‑Fi yedekleme kapalı (ayarlardan açın)"
        }
        if (isCellular(context) && !isWifi(context) && !session.backupOnMobile) {
            return "Mobil veri yedekleme kapalı (ayarlardan açın)"
        }
        return "Ağ uygun değil"
    }

    fun workManagerNetworkType(session: SessionStore): NetworkType {
        return when {
            session.backupOnWifi && session.backupOnMobile -> NetworkType.CONNECTED
            session.backupOnWifi && !session.backupOnMobile -> NetworkType.UNMETERED
            !session.backupOnWifi && session.backupOnMobile -> NetworkType.CONNECTED
            else -> NetworkType.CONNECTED
        }
    }

    fun networkPolicyLabel(session: SessionStore): String {
        return when {
            session.backupOnWifi && session.backupOnMobile -> "Wi‑Fi + mobil veri"
            session.backupOnWifi -> "yalnız Wi‑Fi"
            session.backupOnMobile -> "yalnız mobil veri"
            else -> "ağ kapalı"
        }
    }

    fun bindUploadNetwork(context: Context): Network? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return null
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return null
        val network = cm.activeNetwork ?: return null
        runCatching { cm.bindProcessToNetwork(network) }
        return network
    }

    fun unbindUploadNetwork(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return
        runCatching { cm.bindProcessToNetwork(null) }
    }

    private fun networkAllowed(context: Context, session: SessionStore): Boolean {
        if (!session.backupOnWifi && !session.backupOnMobile) return false
        if (isWifi(context)) return session.backupOnWifi
        if (isCellular(context)) return session.backupOnMobile
        return session.backupOnWifi || session.backupOnMobile
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

    private suspend fun awaitMatchingNetwork(context: Context, session: SessionStore, startedMs: Long) {
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return
        if (allowsUploadNow(context, session, -1L)) return
        suspendCancellableCoroutine { cont ->
            val done = AtomicBoolean(false)
            val request = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .apply {
                    if (session.backupOnWifi && !session.backupOnMobile) {
                        addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                        addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
                    }
                }
                .build()
            val callback = object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) {
                    tryResume(context, session, startedMs, cm, this, done, cont)
                }

                override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                    tryResume(context, session, startedMs, cm, this, done, cont)
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
            tryResume(context, session, startedMs, cm, callback, done, cont)
        }
    }

    private fun tryResume(
        context: Context,
        session: SessionStore,
        startedMs: Long,
        cm: ConnectivityManager,
        callback: ConnectivityManager.NetworkCallback,
        done: AtomicBoolean,
        cont: kotlinx.coroutines.CancellableContinuation<Unit>,
    ) {
        if (hasUsableInternet(context, startedMs) && networkAllowed(context, session) &&
            done.compareAndSet(false, true)
        ) {
            runCatching { cm.unregisterNetworkCallback(callback) }
            cont.resume(Unit)
        }
    }
}
