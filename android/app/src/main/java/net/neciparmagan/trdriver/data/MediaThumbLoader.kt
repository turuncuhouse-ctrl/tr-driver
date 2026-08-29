package net.neciparmagan.trdriver.data

import android.content.ContentUris
import android.content.Context
import android.graphics.Bitmap
import android.graphics.drawable.ColorDrawable
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import android.util.Size
import android.widget.ImageView
import coil.load
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Gallery-style thumbnails: MediaStore [ContentResolver.loadThumbnail] for local
 * images/videos (Coil cannot decode video frames by default).
 */
object MediaThumbLoader {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val placeholder = ColorDrawable(0xFFDCE6F5.toInt())

    fun loadLocal(imageView: ImageView, uri: Uri, cacheKey: String, sizePx: Int = 320) {
        imageView.tag = cacheKey
        imageView.setImageDrawable(placeholder)
        val appCtx = imageView.context.applicationContext
        scope.launch {
            val bmp = withContext(Dispatchers.IO) {
                decodeSystemThumb(appCtx, uri, sizePx)
            }
            if (imageView.tag != cacheKey) return@launch
            if (bmp != null) {
                imageView.setImageBitmap(bmp)
                return@launch
            }
            // SAF / exotic formats: Coil decode as image when system thumb is unavailable.
            imageView.load(uri) {
                size(sizePx, sizePx)
                memoryCacheKey(cacheKey)
                diskCacheKey("local-$cacheKey")
                crossfade(true)
                placeholder(placeholder)
            }
        }
    }

    fun loadRemote(
        imageView: ImageView,
        url: String,
        token: String,
        cacheKey: String,
        sizePx: Int = 320,
        isVideo: Boolean = false,
    ) {
        imageView.tag = cacheKey
        imageView.setImageDrawable(placeholder)
        if (isVideo) {
            // Full video download for a frame is too heavy; keep placeholder + badge.
            return
        }
        imageView.load(url) {
            size(sizePx, sizePx)
            memoryCacheKey(cacheKey)
            diskCacheKey("remote-$cacheKey")
            crossfade(true)
            placeholder(placeholder)
            if (token.isNotBlank()) {
                addHeader("Authorization", "Bearer $token")
            }
        }
    }

    private fun decodeSystemThumb(context: Context, uri: Uri, sizePx: Int): Bitmap? {
        val cr = context.contentResolver
        return runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                cr.loadThumbnail(uri, Size(sizePx, sizePx), null)
            } else {
                val id = ContentUris.parseId(uri)
                val kind = MediaStore.Images.Thumbnails.MINI_KIND
                when {
                    uri.toString().contains("/video/", ignoreCase = true) -> {
                        MediaStore.Video.Thumbnails.getThumbnail(cr, id, kind, null)
                    }
                    else -> MediaStore.Images.Thumbnails.getThumbnail(cr, id, kind, null)
                }
            }
        }.getOrNull()
    }
}
