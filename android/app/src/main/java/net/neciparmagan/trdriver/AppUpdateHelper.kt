package net.neciparmagan.trdriver

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.BuildConfig
import net.neciparmagan.trdriver.data.AndroidVersionInfo
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import java.io.File

object AppUpdateHelper {
    private const val THROTTLE_MS = 12_000L
    private var lastCheckAt = 0L
    private var skippedVersionCode = 0
    private var dialogShowing = false
    private var downloading = false

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
                if (!force && info.versionCode == skippedVersionCode) return@launch
                showUpdateDialog(activity, api, info)
            } catch (e: Exception) {
                if (force) toast(activity, "Güncelleme kontrolü başarısız: ${e.message}")
            }
        }
    }

    private fun showUpdateDialog(activity: MainActivity, api: DriveApi, info: AndroidVersionInfo) {
        if (dialogShowing || activity.isFinishing) return
        dialogShowing = true
        val notes = info.releaseNotes.ifBlank { "Yeni sürüm hazır." }
        AlertDialog.Builder(activity)
            .setTitle("Güncelleme var")
            .setMessage(
                "v${info.versionName} (kod ${info.versionCode})\n\n$notes\n\n" +
                    "Şu an: v${BuildConfig.VERSION_NAME}",
            )
            .setPositiveButton("Güncelle") { _, _ ->
                dialogShowing = false
                startDownload(activity, api, info)
            }
            .setNegativeButton("Sonra") { _, _ ->
                skippedVersionCode = info.versionCode
                dialogShowing = false
            }
            .setOnDismissListener { dialogShowing = false }
            .show()
    }

    private fun startDownload(activity: MainActivity, api: DriveApi, info: AndroidVersionInfo) {
        if (downloading) return
        if (!ensureInstallPermission(activity)) return
        downloading = true
        toast(activity, "Güncelleme indiriliyor…")
        activity.lifecycleScope.launch {
            try {
                val file = withContext(Dispatchers.IO) {
                    api.downloadApkUpdate(info.downloadURL.ifBlank { info.apkPath })
                }
                installApk(activity, file)
            } catch (e: Exception) {
                toast(activity, "İndirme başarısız: ${e.message}")
            } finally {
                downloading = false
            }
        }
    }

    private fun ensureInstallPermission(activity: Activity): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return true
        if (activity.packageManager.canRequestPackageInstalls()) return true
        AlertDialog.Builder(activity)
            .setTitle("Kurulum izni")
            .setMessage("Güncellemek için bu uygulamaya paket kurma izni verin.")
            .setPositiveButton("Ayarlar") { _, _ ->
                val intent = Intent(
                    Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    Uri.parse("package:${activity.packageName}"),
                )
                activity.startActivity(intent)
            }
            .setNegativeButton("İptal", null)
            .show()
        return false
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
