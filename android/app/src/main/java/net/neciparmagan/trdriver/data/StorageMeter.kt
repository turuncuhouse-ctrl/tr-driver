package net.neciparmagan.trdriver.data

/**
 * Pure storage-meter math and labels (no Android dependencies, JVM-testable).
 */
object StorageMeter {
    /** Usage percent clamped to 0–100; 0 when quota is unknown. */
    fun percent(usedBytes: Long, quotaBytes: Long): Int {
        if (quotaBytes <= 0L || usedBytes <= 0L) return 0
        return ((usedBytes * 100) / quotaBytes).coerceIn(0L, 100L).toInt()
    }

    /** One-line "Depolama: …" label; byte rendering is injected for tests. */
    fun summary(usedBytes: Long, quotaBytes: Long, formatBytes: (Long) -> String): String {
        if (quotaBytes <= 0L) return "Depolama: ${formatBytes(usedBytes)} kullanıldı"
        return "Depolama: ${formatBytes(usedBytes)} / ${formatBytes(quotaBytes)} " +
            "(%${percent(usedBytes, quotaBytes)})"
    }
}
