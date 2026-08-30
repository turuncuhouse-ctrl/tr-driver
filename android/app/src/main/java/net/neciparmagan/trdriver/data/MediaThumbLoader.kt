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
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.concurrent.ConcurrentHashMap

/**
 * Gallery-style thumbnails: MediaStore [ContentResolver.loadThumbnail] for local
 * images/videos (Coil cannot decode video frames by default).
 */
object MediaThumbLoader {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val placeholder = ColorDrawable(0xFFDCE6F5.toInt())
    private val activeJobs = ConcurrentHashMap<ImageView, Job>()

    fun loadLocal(imageView: ImageView, uri: Uri, cacheKey: String, sizePx: Int = 320) {
        cancelLoad(imageView)
        imageView.tag = cacheKey
        imageView.setImageDrawable(placeholder)
        val appCtx = imageView.context.applicationContext
        val job = scope.launch {
            val bmp = withContext(Dispatchers.IO) {
                decodeSystemThumb(appCtx, uri, sizePx)
            }
            if (imageView.tag != cacheKey) return@launch
            if (bmp != null) {
                imageView.setImageBitmap(bmp)
                return@launch
            }
            imageView.load(uri) {
                size(sizePx, sizePx)
                memoryCacheKey(cacheKey)
                diskCacheKey("local-$cacheKey")
                crossfade(true)
                placeholder(placeholder)
            }
        }
        activeJobs[imageView] = job
        job.invokeOnCompletion { activeJobs.remove(imageView, job) }
    }

    fun cancelLoad(imageView: ImageView) {
        activeJobs.remove(imageView)?.cancel()
        imageView.tag = null
        imageView.setImageDrawable(placeholder)
    }

    fun loadRemote(
        imageView: ImageView,
        url: String,
        token: String,
        cacheKey: String,
        sizePx: Int = 320,
        isVideo: Boolean = false,
    ) {
        cancelLoad(imageView)
        imageView.tag = cacheKey
        imageView.setImageDrawable(placeholder)
        if (isVideo) {
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
