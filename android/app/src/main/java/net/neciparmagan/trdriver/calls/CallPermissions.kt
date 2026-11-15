package net.neciparmagan.trdriver.calls

import android.Manifest
import android.app.Activity
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.appcompat.app.AlertDialog
import androidx.core.content.ContextCompat

/**
 * Runtime and Android 14+ special permission helpers for LocalTel calling.
 * Every permission request is preceded by a plain-Turkish explanation dialog.
 */
object CallPermissions {

    fun hasMicrophone(context: Context): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED

    fun hasNotifications(context: Context): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
                PackageManager.PERMISSION_GRANTED
        } else {
            true
        }

    fun hasFullScreenIntent(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) return true
        val nm = context.getSystemService(NotificationManager::class.java) ?: return true
        return nm.canUseFullScreenIntent()
    }

    fun hasBluetoothConnect(context: Context): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            ContextCompat.checkSelfPermission(context, Manifest.permission.BLUETOOTH_CONNECT) ==
                PackageManager.PERMISSION_GRANTED
        } else {
            true
        }

    fun showMicrophoneExplanation(activity: Activity, onAccept: () -> Unit) {
        AlertDialog.Builder(activity)
            .setTitle("Mikrofon izni")
            .setMessage(
                "LocalTel üzerinden sesli IP araması yapabilmek ve gelen aramalarda sesinizi " +
                    "iletmek için mikrofon erişimi gereklidir. Ses kaydı yapılmaz, yalnızca canlı görüşme aktarılır.",
            )
            .setPositiveButton("İzin ver") { _, _ -> onAccept() }
            .setNegativeButton("Şimdi değil", null)
            .show()
    }

    fun showFullScreenIntentExplanation(activity: Activity, onAccept: () -> Unit) {
        AlertDialog.Builder(activity)
            .setTitle("Kilit ekranında gelen arama")
            .setMessage(
                "Telefonunuz kilitliyken gelen LocalTel aramalarında büyük Aç / Reddet " +
                    "ekranının gösterilebilmesi için tam ekran bildirim iznini açmanız gerekir.",
            )
            .setPositiveButton("Ayarlara git") { _, _ -> onAccept() }
            .setNegativeButton("Sonra", null)
            .show()
    }

    fun showNotificationsExplanation(activity: Activity, onAccept: () -> Unit) {
        AlertDialog.Builder(activity)
            .setTitle("Arama bildirimleri")
            .setMessage(
                "Gelen çağrıları ve devam eden görüşme kontrolünü görebilmeniz için bildirim izni gereklidir.",
            )
            .setPositiveButton("İzin ver") { _, _ -> onAccept() }
            .setNegativeButton("Şimdi değil", null)
            .show()
    }

    fun openFullScreenIntentSettings(activity: Activity) {
        val intent = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            Intent(
                Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT,
                Uri.parse("package:${activity.packageName}"),
            )
        } else {
            Intent(
                Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                Uri.parse("package:${activity.packageName}"),
            )
        }
        runCatching { activity.startActivity(intent) }
    }
}
