package net.neciparmagan.trdriver.data

import kotlinx.coroutines.delay
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlin.random.Random

/**
 * Adaptive upload gate driven by server /api/files/upload-pace.
 * Falls back to a mild delay when pace is unknown.
 */
object UploadThrottle {
    private val mutex = Mutex()

    @Volatile
    private var delayMs: Long = 350L

    @Volatile
    private var acceptUploads: Boolean = true

    @Volatile
    private var retryAfterSec: Int = 0

    @Volatile
    private var mode: String = "normal"

    fun applyPace(pace: UploadPace) {
        delayMs = pace.delayMs.coerceIn(50, 8_000).toLong()
        acceptUploads = pace.acceptUploads
        retryAfterSec = pace.retryAfterSec.coerceAtLeast(0)
        mode = pace.mode.ifBlank { "normal" }
    }

    fun currentMode(): String = mode

    suspend fun <T> run(block: suspend () -> T): T = mutex.withLock {
        var guard = 0
        while (!acceptUploads && guard < 30) {
            val waitSec = retryAfterSec.coerceIn(1, 15)
            delay(waitSec * 1_000L)
            guard++
        }
        val result = block()
        val jitter = (delayMs / 4).coerceAtLeast(20L)
        val lo = (delayMs - jitter).coerceAtLeast(50L)
        val hi = (delayMs + jitter).coerceAtLeast(lo + 1)
        delay(Random.nextLong(lo, hi + 1))
        result
    }
}
