package net.neciparmagan.trdriver.backup

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import androidx.appcompat.app.AlertDialog
import net.neciparmagan.trdriver.data.SessionStore

/** HyperOS / MIUI and other OEM background restrictions. */
object OemPowerHelper {
    private const val PREFS = "trdriver_oem"
    private const val KEY_BATTERY_PROMPTED = "battery_prompted"

    fun isXiaomiFamily(): Boolean {
        val m = Build.MANUFACTURER.lowercase()
        val b = Build.BRAND.lowercase()
        return m.contains("xiaomi") || m.contains("redmi") || m.contains("poco") ||
            b.contains("xiaomi") || b.contains("redmi") || b.contains("poco")
    }

    fun isIgnoringBatteryOptimizations(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return true
        val pm = context.getSystemService(Context.POWER_SERVICE) as? PowerManager ?: return true
        return pm.isIgnoringBatteryOptimizations(context.packageName)
    }

    fun maybePromptForReliableBackup(context: Context, onDone: (() -> Unit)? = null) {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val session = SessionStore(context)
        if (!session.galleryBackupEnabled) {
            onDone?.invoke()
            return
        }
        val needsBattery = Build.VERSION.SDK_INT >= Build.VERSION_CODES.M &&
            !isIgnoringBatteryOptimizations(context)
        val showXiaomi = isXiaomiFamily()
        if (!needsBattery && !showXiaomi) {
            onDone?.invoke()
            return
        }
        if (!needsBattery && prefs.getBoolean(KEY_BATTERY_PROMPTED, false) && !showXiaomi) {
            onDone?.invoke()
            return
        }
        val msg = buildString {
            append("Galeri yedeğinin arka planda sorunsuz çalışması için:\n\n")
            if (needsBattery) append("• Pil optimizasyonunu kapatın\n")
            if (showXiaomi) {
                append("• Otomatik başlatmayı açın (Xiaomi/Redmi)\n")
                append("• Arka plan kısıtlamasını \"Kısıtlama yok\" yapın\n")
            }
            append("\nÖzellikle yeni HyperOS sürümlerinde bu adımlar gereklidir.")
        }
        AlertDialog.Builder(context)
            .setTitle("Yedekleme için izinler")
            .setMessage(msg)
            .setPositiveButton("Pil optimizasyonu") { _, _ ->
                prefs.edit().putBoolean(KEY_BATTERY_PROMPTED, true).apply()
                openBatterySettings(context)
                onDone?.invoke()
            }
            .apply {
                if (showXiaomi) {
                    setNeutralButton("Xiaomi ayarları") { _, _ ->
                        openXiaomiAutostart(context)
                        onDone?.invoke()
                    }
                }
            }
            .setNegativeButton("Sonra") { _, _ -> onDone?.invoke() }
            .show()
    }

    fun openBatterySettings(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
            data = Uri.parse("package:${context.packageName}")
        }
        runCatching { context.startActivity(intent) }
            .onFailure {
                runCatching {
                    context.startActivity(
                        Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS),
                    )
                }
            }
    }

    fun openXiaomiAutostart(context: Context) {
        val intents = listOf(
            Intent().setComponent(
                ComponentName(
                    "com.miui.securitycenter",
                    "com.miui.permcenter.autostart.AutoStartManagementActivity",
                ),
            ),
            Intent().setComponent(
                ComponentName(
                    "com.miui.securitycenter",
                    "com.miui.powercenter.PowerSettings",
                ),
            ),
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
                data = Uri.parse("package:${context.packageName}")
            },
        )
        for (intent in intents) {
            intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            if (runCatching { context.startActivity(intent); true }.getOrDefault(false)) return
        }
    }
}
