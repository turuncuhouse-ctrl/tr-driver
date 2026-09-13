package net.neciparmagan.trdriver.data

import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Locale
import java.util.TimeZone

/** Resolve a stable millisecond timestamp for cloud timeline grouping. */
object FileEntryDates {
    fun resolveSortMs(entry: FileEntry, pathYear: Int?, pathMonth: Int?): Long {
        parseInstantMs(entry.clientModifiedAt)
            ?.takeIf { it > 0L }
            ?.let { return it }
        parseInstantMs(entry.createdAt)
            ?.takeIf { it > 0L }
            ?.let { return it }
        parseInstantMs(entry.updatedAt)
            ?.takeIf { it > 0L }
            ?.let { return it }
        if (pathYear != null && pathMonth != null) {
            val c = Calendar.getInstance()
            c.clear()
            c.set(Calendar.YEAR, pathYear)
            c.set(Calendar.MONTH, pathMonth - 1)
            c.set(Calendar.DAY_OF_MONTH, 15)
            return c.timeInMillis
        }
        if (pathYear != null) {
            val c = Calendar.getInstance()
            c.clear()
            c.set(Calendar.YEAR, pathYear)
            c.set(Calendar.MONTH, Calendar.JUNE)
            c.set(Calendar.DAY_OF_MONTH, 15)
            return c.timeInMillis
        }
        return 0L
    }

    fun parseInstantMs(raw: String?): Long? {
        val s = raw?.trim().orEmpty()
        if (s.isEmpty()) return null
        // Instant / OffsetDateTime style from Go JSON
        runCatching {
            return java.time.Instant.parse(s).toEpochMilli()
        }
        runCatching {
            return java.time.OffsetDateTime.parse(s).toInstant().toEpochMilli()
        }
        val patterns = arrayOf(
            "yyyy-MM-dd'T'HH:mm:ss.SSSXXX",
            "yyyy-MM-dd'T'HH:mm:ssXXX",
            "yyyy-MM-dd'T'HH:mm:ss'Z'",
            "yyyy-MM-dd HH:mm:ss",
            "yyyy-MM-dd",
        )
        for (p in patterns) {
            runCatching {
                val fmt = SimpleDateFormat(p, Locale.US).apply {
                    timeZone = TimeZone.getTimeZone("UTC")
                    isLenient = true
                }
                return fmt.parse(s)?.time
            }
        }
        return null
    }
}
