package net.neciparmagan.trdriver.backup

import java.util.concurrent.atomic.AtomicBoolean

/**
 * A single global upload lock.
 *
 * The persistent keep-alive service, the WorkManager fallback worker and the
 * manual "Yedekle" action can all be triggered at the same time (boot + Wi-Fi +
 * periodic job). Without this lock two coroutines would upload the same media
 * twice and both would try to update progress prefs.
 */
object BackupRunLock {
    private val active = AtomicBoolean(false)

    fun tryAcquire(): Boolean = active.compareAndSet(false, true)

    fun release() {
        active.set(false)
    }

    fun isHeld(): Boolean = active.get()
}
