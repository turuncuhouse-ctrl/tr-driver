package net.neciparmagan.trdriver.data

import android.content.Context
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

/**
 * Retries uploads when Wi‑Fi ↔ mobile (or brief drops) kill the TCP stream.
 */
object UploadRetry {
    suspend fun <T> run(
        context: Context,
        attempts: Int = 8,
        fileBytes: Long = -1L,
        onRetry: ((attempt: Int, error: Throwable) -> Unit)? = null,
        block: suspend () -> T,
    ): T {
        val session = SessionStore(context)
        var last: Exception? = null
        for (attempt in 1..attempts) {
            try {
                UploadNetworkGate.awaitUploadAllowed(context, session, fileBytes)
                return block()
            } catch (e: CancellationException) {
                throw e
            } catch (e: UploadNetworkBlockedException) {
                throw e
            } catch (e: SkipUploadException) {
                throw e
            } catch (e: DuplicateNameException) {
                throw e
            } catch (e: Exception) {
                last = e
                if (!isTransient(e) || attempt >= attempts) throw e
                onRetry?.invoke(attempt, e)
                val extra = (e as? HttpStatusIOException)?.retryAfterSec?.coerceIn(1, 30) ?: 0
                delay(750L * attempt + extra * 1000L)
            }
        }
        throw last ?: java.io.IOException("Yükleme başarısız")
    }

    fun isTransient(error: Throwable): Boolean {
        var cur: Throwable? = error
        while (cur != null) {
            when (cur) {
                is HttpStatusIOException -> if (cur.code == 429 || cur.code in 500..599) return true
                is java.net.UnknownHostException,
                is java.net.ConnectException,
                is java.net.SocketTimeoutException,
                is java.net.SocketException,
                is java.io.InterruptedIOException,
                is javax.net.ssl.SSLException,
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
                msg.contains("server overloaded") ||
                msg.contains("network changed") ||
                msg.contains("enetunreach") ||
                msg.contains("econnreset")
            ) {
                return true
            }
            cur = cur.cause
        }
        return false
    }
}
