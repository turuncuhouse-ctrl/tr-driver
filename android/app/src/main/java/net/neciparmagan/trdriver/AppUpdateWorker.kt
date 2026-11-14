package net.neciparmagan.trdriver

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters

/**
 * Periodic in-app update check.
 *
 * Previously the update manifest was only read from MainActivity.onResume, so
 * a user who never opened the app never learned about a new release. This
 * worker only NOTIFIES; tapping the notification opens MainActivity, where the
 * existing AppUpdateHelper download/install flow runs with the required
 * REQUEST_INSTALL_PACKAGES permission UI.
 */
class AppUpdateWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result = try {
        AppUpdateHelper.checkFromBackground(applicationContext)
        Result.success()
    } catch (e: Exception) {
        Log.w(TAG, "background update check failed: ${e.message}")
        Result.retry()
    }

    companion object {
        private const val TAG = "AppUpdateWorker"
        const val UNIQUE_WORK = "trdriver_app_update_check"
    }
}
