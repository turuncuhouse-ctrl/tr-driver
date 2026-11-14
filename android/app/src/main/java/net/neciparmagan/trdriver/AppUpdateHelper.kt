package net.neciparmagan.trdriver

import android.app.Activity
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.core.app.NotificationCompat
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.AndroidVersionInfo
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import java.io.File
import java.security.MessageDigest

/**
 * Auto-update: checks server, downloads APK in background, notifies for one-tap install.
 * Fully silent install (no user tap) requires device-owner / enterprise — not available for normal installs.
 */
object AppUpdateHelper {
    private const val THROTTLE_MS = 12_000L
    private const val CHANNEL_ID = "trdriver_updates"
    private const val NOTIFY_ID = 4201

    /**
     * SHA-256 of the release certificate used by the currently published APK
     * (subject CN=Necip Armagan, O=Kasanibil, C=TR; new keystore from 2026-11-15).
     * Pinning it makes a tampered/re-signed APK from a compromised server
     * un-installable, and guarantees updates stay installable over the existing
     * installation. Existing installs signed with the previous certificate must
     * uninstall and reinstall once.
     */
    private const val PINNED_SIGNER_SHA256 =
        "F1311C3AA258FFCD4788C4392DA3CFE06B9D38C16C14D1C4DF1923DDEDE75723"
    private var lastCheckAt = 0L
    private var skippedVersionCode = 0
    private var dialogShowing = false
    private var downloading = false
    private var pendingDownloadInfo: AndroidVersionInfo? = null
    private var pendingInstallFile: File? = null

    fun check(
        activity: MainActivity,
        force: Boolean = false,
        silentIfCurrent: Boolean = true,
    ) {
        val session = SessionStore(activity)
        if (session.serverUrl.isBlank()) return
        val now = System.currentTimeMillis()
        if (!force && now - lastCheckAt < THROTTLE_MS) return
        lastCheckAt = now

        activity.lifecycleScope.launch {
            try {
                val api = DriveApi(session, activity.applicationContext)
                val info = withContext(Dispatchers.IO) { api.fetchAndroidVersion() }
                if (!info.apkAvailable || info.versionCode <= 0) {
                    if (force) toast(activity, "Sunucuda APK sürümü bulunamadı")
                    return@launch
                }
                if (info.versionCode <= BuildConfig.VERSION_CODE) {
                    if (force || !silentIfCurrent) {
                        toast(activity, "Uygulama güncel (v${BuildConfig.VERSION_NAME})")
                    }
                    return@launch
                }
                // A version below minSupportedCode must not be skippable.
                val forced = info.minSupportedCode > BuildConfig.VERSION_CODE
                if (!force && !forced && info.versionCode == skippedVersionCode) return@launch

                if (force || forced) {
                    showUpdateDialog(activity, api, info)
                } else {
                    // Silent auto-download; user only taps install notification.
                    startDownload(activity, api, info, silent = true)
                }
            } catch (e: Exception) {
                if (force) toast(activity, "Güncelleme kontrolü başarısız: ${e.message}")
            }
        }
    }

    /**
     * Background check used by AppUpdateWorker. Does NOT download or install:
     * it only raises a notification so the user can open the app and complete
     * the permission-gated install flow.
     */
    suspend fun checkFromBackground(context: Context) {
        val app = context.applicationContext
        val session = SessionStore(app)
        if (session.serverUrl.isBlank()) return
        val api = DriveApi(session, app)
        val info = withContext(Dispatchers.IO) { api.fetchAndroidVersion() }
        if (!info.apkAvailable || info.versionCode <= 0) return
        if (info.versionCode <= BuildConfig.VERSION_CODE) return
        notifyUpdateAvailable(app, info)
    }

    private fun notifyUpdateAvailable(context: Context, info: AndroidVersionInfo) {
        ensureUpdateChannel(context)
        val forced = info.minSupportedCode > BuildConfig.VERSION_CODE
        val open = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
            putExtra("tr_install_update", true)
        }
        val pi = PendingIntent.getActivity(
            context,
            92,
            open,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val n = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_backup)
            .setContentTitle(
                if (forced) "TR Driver zorunlu güncelleme" else "TR Driver güncelleme var",
            )
            .setContentText("v${info.versionName} hazır — kurmak için dokunun")
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setPriority(
                if (forced) NotificationCompat.PRIORITY_HIGH
                else NotificationCompat.PRIORITY_DEFAULT,
            )
            .build()
        context.getSystemService(NotificationManager::class.java).notify(NOTIFY_ID, n)
    }

    /**
     * Verify the downloaded APK's signing certificate against the pinned
     * release cert before offering it for installation. Android already checks
     * this at install time, but pinning also blocks a server-side APK swap.
     *
     * If the OS cannot expose signers (older framework / v2-only archive), we
     * fail open — Android's own installer verification still applies — so a
     * framework quirk can never permanently block legitimate updates.
     */
    private fun verifyReleaseSignature(context: Context, file: File): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) return true
        return try {
            val info = context.packageManager.getPackageArchiveInfo(
                file.absolutePath,
                PackageManager.GET_SIGNING_CERTIFICATES,
            ) ?: return true
            val signers = info.signingInfo?.apkContentsSigners
            if (signers.isNullOrEmpty()) return true
            val md = MessageDigest.getInstance("SHA-256")
            signers.any { sig ->
                val hex = md.digest(sig.toByteArray()).joinToString("") { b ->
                    "%02X".format(b.toInt() and 0xFF)
                }
                hex.equals(PINNED_SIGNER_SHA256, ignoreCase = true)
            }
        } catch (e: Exception) {
            true
        }
    }

    fun onMainResume(activity: MainActivity) {
        val info = pendingDownloadInfo
        if (info != null) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O &&
                !activity.packageManager.canRequestPackageInstalls()
            ) {
                return
            }
            pendingDownloadInfo = null
            val api = DriveApi(SessionStore(activity), activity.applicationContext)
            startDownload(activity, api, info, silent = true)
            return
        }
        val file = pendingInstallFile
        if (file != null && file.exists()) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O &&
                !activity.packageManager.canRequestPackageInstalls()
            ) {
                return
            }
            pendingInstallFile = null
            installApk(activity, file)
        }
    }

    private fun showUpdateDialog(activity: MainActivity, api: DriveApi, info: AndroidVersionInfo) {
        if (dialogShowing || activity.isFinishing) return
        dialogShowing = true
        val notes = info.releaseNotes.ifBlank { "Yeni sürüm hazır." }
        val forced = info.minSupportedCode > BuildConfig.VERSION_CODE
        AlertDialog.Builder(activity)
            .setTitle(if (forced) "Zorunlu güncelleme" else "Güncelleme var")
            .setMessage(
                "v${info.versionName} (kod ${info.versionCode})\n\n$notes\n\n" +
                    "Şu an: v${BuildConfig.VERSION_NAME}" +
                    (if (forced) "\n\nBu sürüm artık desteklenmiyor; güncelleme önerilir." else ""),
            )
            .setPositiveButton("Güncelle") { _, _ ->
                dialogShowing = false
                startDownload(activity, api, info, silent = false)
            }
            .setNegativeButton("Sonra") { _, _ ->
                skippedVersionCode = info.versionCode
                dialogShowing = false
            }
            .setOnDismissListener { dialogShowing = false }
            .show()
    }

    private fun startDownload(
        activity: MainActivity,
        api: DriveApi,
        info: AndroidVersionInfo,
        silent: Boolean,
    ) {
        if (downloading) return
        if (!ensureInstallPermission(activity, info)) return
        downloading = true
        if (!silent) toast(activity, "Güncelleme indiriliyor…")
        activity.lifecycleScope.launch {
            try {
                val file = withContext(Dispatchers.IO) {
                    api.downloadApkUpdate(info.downloadURL.ifBlank { info.apkPath })
                }
                if (!verifyReleaseSignature(activity.applicationContext, file)) {
                    runCatching { file.delete() }
                    if (!silent) {
                        toast(activity, "Güncelleme imzası doğrulanamadı; kurulum iptal edildi")
                    }
                    return@launch
                }
                notifyReadyToInstall(activity, file, info)
                if (!silent) installApk(activity, file)
            } catch (e: Exception) {
                if (!silent) toast(activity, "İndirme başarısız: ${e.message}")
            } finally {
                downloading = false
            }
        }
    }

    private fun ensureInstallPermission(activity: Activity, info: AndroidVersionInfo): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return true
        if (activity.packageManager.canRequestPackageInstalls()) return true
        pendingDownloadInfo = info
        AlertDialog.Builder(activity)
            .setTitle("Kurulum izni")
            .setMessage(
                "Otomatik güncelleme için paket kurma izni gerekli. " +
                    "Ayarlardan dönünce indirme devam eder.",
            )
            .setPositiveButton("Ayarlar") { _, _ ->
                val intent = Intent(
                    Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    Uri.parse("package:${activity.packageName}"),
                )
                activity.startActivity(intent)
            }
            .setNegativeButton("İptal") { _, _ ->
                pendingDownloadInfo = null
            }
            .show()
        return false
    }

    private fun notifyReadyToInstall(context: Context, file: File, info: AndroidVersionInfo) {
        pendingInstallFile = file
        ensureUpdateChannel(context)
        val open = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
            putExtra("tr_install_update", true)
        }
        val pi = PendingIntent.getActivity(
            context,
            91,
            open,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val n = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_backup)
            .setContentTitle("TR Driver güncelleme hazır")
            .setContentText("v${info.versionName} — kurmak için dokunun")
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .build()
        context.getSystemService(NotificationManager::class.java).notify(NOTIFY_ID, n)
    }

    private fun ensureUpdateChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, "Uygulama güncellemeleri", NotificationManager.IMPORTANCE_DEFAULT),
        )
    }

    private fun installApk(activity: Activity, file: File) {
        val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.files", file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        try {
            activity.startActivity(intent)
        } catch (e: Exception) {
            toast(activity, "Kurulum açılamadı: ${e.message}")
        }
    }

    private fun toast(activity: Activity, msg: String) {
        Toast.makeText(activity, msg, Toast.LENGTH_LONG).show()
    }
}
