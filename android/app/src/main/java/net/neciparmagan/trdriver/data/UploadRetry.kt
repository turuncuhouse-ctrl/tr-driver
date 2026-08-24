package net.neciparmagan.trdriver.data

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import java.io.IOException
import java.io.InterruptedIOException
import java.net.ConnectException
import java.net.SocketException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume
import javax.net.ssl.SSLException

/**
 * Retries uploads when Wi‑Fi ↔ mobile (or brief drops) kill the TCP stream.
 * Multipart uploads restart the current file; already-finished files stay done.
 */
object UploadRetry {
    suspend fun <T> run(
        context: Context,
        attempts: Int = 6,
        onRetry: ((attempt: Int, error: Throwable) -> Unit)? = null,
        block: suspend () -> T,
    ): T {
        var last: Exception? = null
        for (attempt in 1..attempts) {
            try {
                awaitNetwork(context)
                return block()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                last = e
                if (!isTransient(e) || attempt >= attempts) throw e
                onRetry?.invoke(attempt, e)
                val extra = (e as? HttpStatusIOException)?.retryAfterSec?.coerceIn(1, 30) ?: 0
                delay(700L * attempt + extra * 1000L)
                // Re-read server pace after overload / network blip.
                runCatching {
                    // best-effort; DriveApi.refresh is suspend — caller also refreshes
                }
            }
        }
        throw last ?: IOException("Yükleme başarısız")
    }

    fun isTransient(error: Throwable): Boolean {
        var cur: Throwable? = error
        while (cur != null) {
            when (cur) {
                is HttpStatusIOException -> if (cur.code == 429 || cur.code in 500..599) return true
                is UnknownHostException,
                is ConnectException,
                is SocketTimeoutException,
                is SocketException,
                is InterruptedIOException,
                is SSLException,
                -> return true
            }
            val msg = cur.message?.lowercase().orEmpty()
            if (msg.contains("failed to connect") ||
                msg.contains("connection reset") ||
                msg.contains("connection abort") ||
                msg.contains("broken pipe") ||
                msg.contains("software caused connection") ||
                msg.contains("network is unreachable") ||
                msg.contains("timeout") ||
                msg.contains("stream was reset") ||
                msg.contains("unable to resolve") ||
                msg.contains("sunucu yoğun") ||
                msg.contains("server overloaded")
            ) {
                return true
            }
            cur = cur.cause
        }
        return false
    }

    private suspend fun awaitNetwork(context: Context, timeoutMs: Long = 45_000L) {
        val cm = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return
        if (hasInternet(cm)) return
        withTimeoutOrNull(timeoutMs) {
            suspendCancellableCoroutine { cont ->
                val done = AtomicBoolean(false)
                val request = NetworkRequest.Builder()
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                    .build()
                val callback = object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(network: Network) {
                        if (done.compareAndSet(false, true)) {
                            runCatching { cm.unregisterNetworkCallback(this) }
                            cont.resume(Unit)
                        }
                    }

                    override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                        if (caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
                            done.compareAndSet(false, true)
                        ) {
                            runCatching { cm.unregisterNetworkCallback(this) }
                            cont.resume(Unit)
                        }
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
                if (hasInternet(cm) && done.compareAndSet(false, true)) {
                    runCatching { cm.unregisterNetworkCallback(callback) }
                    cont.resume(Unit)
                }
            }
        }
    }

    private fun hasInternet(cm: ConnectivityManager): Boolean {
        val network = cm.activeNetwork ?: return false
        val caps = cm.getNetworkCapabilities(network) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
    }
}
