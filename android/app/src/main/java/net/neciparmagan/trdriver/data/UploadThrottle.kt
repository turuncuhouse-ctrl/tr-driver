package net.neciparmagan.trdriver.data

import kotlinx.coroutines.delay
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlin.random.Random

/**
 * Global single-flight upload gate: at most one active upload,
 * then a very short pause so the server is not flooded.
 */
object UploadThrottle {
    private val mutex = Mutex()

    suspend fun <T> run(block: suspend () -> T): T = mutex.withLock {
        val result = block()
        delay(Random.nextLong(100L, 401L))
        result
    }
}
