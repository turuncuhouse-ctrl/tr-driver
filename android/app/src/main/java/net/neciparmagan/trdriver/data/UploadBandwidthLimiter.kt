package net.neciparmagan.trdriver.data

import android.os.SystemClock

/**
 * Caps upload throughput so Wi‑Fi stays usable for WhatsApp and other apps
 * while gallery backup runs in the background.
 */
object UploadBandwidthLimiter {
    /** ~640 KB/s — leaves headroom on typical home Wi‑Fi. */
    @Volatile
    var bytesPerSecond: Long = 640L * 1024L

    /** Slightly slower for automatic gallery backup. */
    @Volatile
    var backupBytesPerSecond: Long = 480L * 1024L

    @Volatile
    var backupMode: Boolean = false

    fun paceAfterWrite(bytesWritten: Int, window: Window) {
        if (bytesWritten <= 0) return
        val limit = if (backupMode) backupBytesPerSecond else bytesPerSecond
        if (limit <= 0L) return
        window.bytes += bytesWritten
        val elapsed = SystemClock.elapsedRealtime() - window.startMs
        if (elapsed <= 0L) return
        val expectedMs = (window.bytes * 1000L) / limit
        if (expectedMs > elapsed) {
            val sleepMs = (expectedMs - elapsed).coerceAtMost(2_000L)
            if (sleepMs > 0L) {
                try {
                    Thread.sleep(sleepMs)
                } catch (_: InterruptedException) {
                    Thread.currentThread().interrupt()
                }
            }
        }
        // Reset window every ~1s so pacing stays accurate.
        if (SystemClock.elapsedRealtime() - window.startMs >= 1_000L) {
            window.startMs = SystemClock.elapsedRealtime()
            window.bytes = 0L
        }
    }

    class Window {
        var startMs: Long = SystemClock.elapsedRealtime()
        var bytes: Long = 0L
    }
}
