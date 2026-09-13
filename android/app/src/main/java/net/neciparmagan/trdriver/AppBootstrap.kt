package net.neciparmagan.trdriver

import android.Manifest
import android.app.Activity
import android.app.role.RoleManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Settings
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.core.content.ContextCompat
import androidx.core.content.pm.ShortcutInfoCompat
import androidx.core.content.pm.ShortcutManagerCompat
import androidx.core.graphics.drawable.IconCompat
import net.neciparmagan.trdriver.data.MediaAccess

/**
 * First-run: request key permissions, pin TR Galeri, offer default gallery role.
 */
object AppBootstrap {
    private const val PREFS = "trdriver_bootstrap"
    private const val KEY_DONE = "bootstrap_v1"
    private const val KEY_GALLERY_PIN = "gallery_pin_asked"
    private const val KEY_DEFAULT_GALLERY = "default_gallery_asked"

    fun neededPermissions(context: Context): Array<String> {
        val out = LinkedHashSet<String>()
        out += MediaAccess.mediaPermissionsForRequest().toList()
        out += MediaAccess.notificationPermissionOrEmpty().toList()
        val optional = listOf(
            Manifest.permission.READ_SMS,
            Manifest.permission.READ_CALL_LOG,
            Manifest.permission.CAMERA,
        )
        for (p in optional) {
            if (ContextCompat.checkSelfPermission(context, p) != PackageManager.PERMISSION_GRANTED) {
                out += p
            }
        }
        return out.filter {
            ContextCompat.checkSelfPermission(context, it) != PackageManager.PERMISSION_GRANTED
        }.toTypedArray()
    }

    fun maybePinGalleryShortcut(activity: Activity) {
        val prefs = activity.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        if (prefs.getBoolean(KEY_GALLERY_PIN, false)) return
        prefs.edit().putBoolean(KEY_GALLERY_PIN, true).apply()
        if (!ShortcutManagerCompat.isRequestPinShortcutSupported(activity)) return
        val shortcut = ShortcutInfoCompat.Builder(activity, "tr_galeri_home")
            .setShortLabel(activity.getString(R.string.gallery_launcher_name))
            .setLongLabel(activity.getString(R.string.gallery_launcher_name))
            .setIcon(IconCompat.createWithResource(activity, R.drawable.ic_gallery_app))
            .setIntent(
                Intent(activity, PhotosLibraryActivity::class.java).apply {
                    action = Intent.ACTION_MAIN
                    addCategory(Intent.CATEGORY_LAUNCHER)
                    flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
                },
            )
            .build()
        ShortcutManagerCompat.requestPinShortcut(activity, shortcut, null)
    }

    fun maybeOfferDefaultGallery(activity: Activity) {
        val prefs = activity.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        if (prefs.getBoolean(KEY_DEFAULT_GALLERY, false)) return
        prefs.edit().putBoolean(KEY_DEFAULT_GALLERY, true).apply()
        AlertDialog.Builder(activity)
            .setTitle("TR Galeri varsayılan olsun mu?")
            .setMessage(
                "Fotoğraf açarken ve galeri seçerken TR Galeri kullanılabilir. " +
                    "Ayarlar → Varsayılan uygulamalar → Galeri bölümünden TR Galeri’yi seçin.",
            )
            .setPositiveButton("Ayarları aç") { _, _ ->
                openDefaultAppsSettings(activity)
            }
            .setNegativeButton("Sonra", null)
            .show()
    }

    fun openDefaultAppsSettings(context: Context) {
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                context.startActivity(
                    Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                )
                return
            }
        } catch (_: Exception) { /* fall through */ }
        try {
            context.startActivity(
                Intent(Settings.ACTION_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
        } catch (_: Exception) {
            Toast.makeText(context, "Ayarlar açılamadı", Toast.LENGTH_SHORT).show()
        }
    }

    fun requestGalleryRole(activity: Activity): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) return false
        return try {
            val rm = activity.getSystemService(RoleManager::class.java) ?: return false
            // ROLE_GALLERY added in API 33 (Tiramisu)
            if (Build.VERSION.SDK_INT >= 33) {
                val role = "android.app.role.GALLERY"
                if (rm.isRoleAvailable(role) && !rm.isRoleHeld(role)) {
                    activity.startActivityForResult(rm.createRequestRoleIntent(role), 9911)
                    return true
                }
            }
            false
        } catch (_: Exception) {
            false
        }
    }

    fun markBootstrapped(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit().putBoolean(KEY_DONE, true).apply()
    }

    fun isBootstrapped(context: Context): Boolean =
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(KEY_DONE, false)
}
