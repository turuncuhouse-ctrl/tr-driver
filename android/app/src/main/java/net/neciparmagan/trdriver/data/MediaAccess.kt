package net.neciparmagan.trdriver.data

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.ContextCompat

/** Gallery backup / photos access across Android 8–14+. */
object MediaAccess {
    fun mediaPermissionsForRequest(): Array<String> {
        return if (Build.VERSION.SDK_INT >= 33) {
            buildList {
                add(Manifest.permission.READ_MEDIA_IMAGES)
                add(Manifest.permission.READ_MEDIA_VIDEO)
                if (Build.VERSION.SDK_INT >= 34) {
                    add(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED)
                }
            }.toTypedArray()
        } else {
            arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
        }
    }

    /** Optional; request separately so notification grant cannot enable backup alone. */
    fun notificationPermissionOrEmpty(): Array<String> {
        return if (Build.VERSION.SDK_INT >= 33) {
            arrayOf(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            emptyArray()
        }
    }

    /**
     * True when the app can read at least some photos/videos.
     * Android 14+: partial “selected photos” access counts as granted.
     */
    fun hasMediaAccess(context: Context): Boolean {
        if (Build.VERSION.SDK_INT >= 33) {
            val images = granted(context, Manifest.permission.READ_MEDIA_IMAGES)
            val video = granted(context, Manifest.permission.READ_MEDIA_VIDEO)
            if (images && video) return true
            if (Build.VERSION.SDK_INT >= 34) {
                return granted(context, Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED)
            }
            return images || video
        }
        return granted(context, Manifest.permission.READ_EXTERNAL_STORAGE)
    }

    fun resolveContentLength(context: Context, uri: android.net.Uri, hint: Long): Long {
        if (hint > 0L) return hint
        return runCatching {
            context.contentResolver.openAssetFileDescriptor(uri, "r")?.use { afd ->
                afd.length.takeIf { it > 0L }
            }
        }.getOrNull() ?: -1L
    }

    private fun granted(context: Context, permission: String): Boolean {
        return ContextCompat.checkSelfPermission(context, permission) ==
            PackageManager.PERMISSION_GRANTED
    }
}
