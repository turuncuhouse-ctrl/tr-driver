package net.neciparmagan.trdriver.backup

import android.content.Context
import android.os.Build
import android.util.Log
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.OutOfQuotaPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.withTimeoutOrNull
import net.neciparmagan.trdriver.AppUpdateWorker
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import net.neciparmagan.trdriver.widget.BackupStatusWidget
import java.util.concurrent.TimeUnit

/**
 * Single, idempotent scheduler for Android gallery backup.
 *
 * Android (and especially HyperOS / MIUI) kills processes, clears deferred work
 * and refuses background foreground-service starts. Reliability therefore comes
 * from redundant triggers that ALL funnel through ensure():
 *
 *  - Application.onCreate
 *  - BOOT_COMPLETED / LOCKED_BOOT_COMPLETED / MY_PACKAGE_REPLACED / quick-boot
 *  - every Activity resume and the backup on/off switch
 *  - Wi-Fi / Ethernet becoming available (WifiBackupWatcher)
 *  - a 15-minute WorkManager periodic watchdog
 *  - MediaStore content changes observed by the keep-alive foreground service
 *
 * Work itself runs in TrKeepAliveService (persistent, user-visible FGS) while it
 * is alive. GalleryBackupWorker is the bounded fallback when the FGS cannot be
 * started from the background (Android 12+).
 */
object BackupController {
    private const val TAG = "BackupController"

    const val UNIQUE_PERIODIC = "trdriver_gallery_periodic"
    const val UNIQUE_ONCE = "trdriver_gallery_once"
    const val UNIQUE_CONTINUE = "trdriver_gallery_continue"

    private const val PREFS = "trdriver_backup_sched"
    private const val KEY_WATCHDOG_V2 = "watchdog_v2_armed"

    /** Conflated: dozens of triggers collapse into a single wake-up for the service loop. */
    private val wake = Channel<Unit>(Channel.CONFLATED)

    /** Wake the persistent service loop without starting any new component. */
    fun requestKick() {
        runCatching { wake.trySend(Unit) }
    }

    /** Suspend until a trigger arrives or the watchdog timeout expires. */
    suspend fun awaitWake(timeoutMs: Long) {
        runCatching {
            withTimeoutOrNull(timeoutMs.coerceAtLeast(1_000L)) { wake.receive() }
        }
    }

    /**
     * Re-arm every trigger. Safe to call hundreds of times.
     *
     * @param startForegroundService only pass true from a context where Android
     * allows an FGS launch (visible Activity, BOOT_COMPLETED). Network callbacks
     * and workers must pass false, otherwise Android 12+ throws
     * ForegroundServiceStartNotAllowedException.
     */
    fun ensure(context: Context, startForegroundService: Boolean = false) {
        val app = context.applicationContext
        val session = SessionStore(app)
        val wm = WorkManager.getInstance(app)

        // Self-update checks run regardless of backup state — even logged-out.
        // The version manifest endpoint is public, so no session is required.
        val updateCheck = PeriodicWorkRequestBuilder<AppUpdateWorker>(12, TimeUnit.HOURS)
            .setConstraints(
                Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build(),
            )
            .build()
        wm.enqueueUniquePeriodicWork(
            AppUpdateWorker.UNIQUE_WORK,
            ExistingPeriodicWorkPolicy.KEEP,
            updateCheck,
        )

        if (!session.anyBackupEnabled() || !session.isLoggedIn) {
            wm.cancelUniqueWork(UNIQUE_PERIODIC)
            wm.cancelUniqueWork(UNIQUE_ONCE)
            wm.cancelUniqueWork(UNIQUE_CONTINUE)
            session.clearBackupProgress(message = session.lastBackupMessage.ifBlank { "Yedek kapalı" })
            BackupStatusWidget.refreshAll(app)
            TrKeepAliveService.stop(app)
            return
        }

        // KEEP (not UPDATE): re-enqueueing with UPDATE on every Wi-Fi callback would
        // keep resetting nextRunTime and the periodic watchdog would never fire.
        //
        // One exception: devices upgraded from the old 30-minute schedule get a
        // single UPDATE so they migrate to the 15-minute watchdog exactly once.
        val schedPrefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val periodicPolicy = if (schedPrefs.getBoolean(KEY_WATCHDOG_V2, false)) {
            ExistingPeriodicWorkPolicy.KEEP
        } else {
            schedPrefs.edit().putBoolean(KEY_WATCHDOG_V2, true).apply()
            ExistingPeriodicWorkPolicy.UPDATE
        }
        val periodic = PeriodicWorkRequestBuilder<GalleryBackupWorker>(15, TimeUnit.MINUTES)
            .setConstraints(constraints(session))
            .build()
        wm.enqueueUniquePeriodicWork(UNIQUE_PERIODIC, periodicPolicy, periodic)
        enqueueOnce(app, ExistingWorkPolicy.KEEP)

        if (startForegroundService) {
            TrKeepAliveService.startIfNeeded(app)
        }
        requestKick()
    }

    /** User tapped "Yedekle" — works from any API level. */
    fun runNow(context: Context) {
        val app = context.applicationContext
        GalleryBackupEngine.invalidateScanCache()
        val session = SessionStore(app)
        if (!session.isLoggedIn) return
        if (!session.anyBackupEnabled()) session.galleryBackupEnabled = true
        session.updateBackupProgress(
            active = true,
            currentFile = "",
            doneCount = session.backupDoneCount,
            pendingCount = session.backupPendingCount.coerceAtLeast(1),
            message = "Yedek başlatılıyor…",
        )
        BackupStatusWidget.refreshAll(app)
        ensure(app, startForegroundService = true)
        requestKick()
    }

    /**
     * Schedule the next bounded fallback batch.
     * WorkManager forbids combining setExpedited() with setInitialDelay() —
     * that IllegalArgumentException used to crash Wi-Fi resume.
     */
    fun scheduleContinue(context: Context, delaySeconds: Long = 1L) {
        val app = context.applicationContext
        val session = SessionStore(app)
        if (!session.anyBackupEnabled() || !session.isLoggedIn) return
        val delay = delaySeconds.coerceAtLeast(0L)
        val builder = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
            .setConstraints(constraints(session))
        if (delay > 0L) {
            builder.setInitialDelay(delay, TimeUnit.SECONDS)
        } else if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            builder.setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
        }
        runCatching {
            WorkManager.getInstance(app).enqueueUniqueWork(
                UNIQUE_CONTINUE,
                ExistingWorkPolicy.REPLACE,
                builder.build(),
            )
        }.onFailure { e -> Log.e(TAG, "scheduleContinue failed delay=$delay", e) }
    }

    private fun enqueueOnce(context: Context, policy: ExistingWorkPolicy) {
        val app = context.applicationContext
        val builder = OneTimeWorkRequestBuilder<GalleryBackupWorker>()
            .setConstraints(constraints(SessionStore(app)))
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            builder.setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
        }
        runCatching {
            WorkManager.getInstance(app).enqueueUniqueWork(UNIQUE_ONCE, policy, builder.build())
        }.onFailure { e -> Log.e(TAG, "enqueueOnce failed", e) }
    }

    private fun constraints(session: SessionStore) = Constraints.Builder()
        .setRequiredNetworkType(UploadNetworkGate.workManagerNetworkType(session))
        .build()
}
