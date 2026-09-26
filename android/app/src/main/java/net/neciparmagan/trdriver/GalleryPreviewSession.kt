package net.neciparmagan.trdriver

import android.app.Activity
import android.content.Intent
import android.net.Uri
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.LocalMedia

data class GalleryPreviewItem(
    val key: String,
    val name: String,
    val mime: String,
    val localUri: String? = null,
    val remoteId: String? = null,
    val isVideo: Boolean = false,
) {
    fun parsedLocalUri(): Uri? = localUri?.let { runCatching { Uri.parse(it) }.getOrNull() }

    companion object {
        fun fromLocal(media: LocalMedia, remoteId: String?): GalleryPreviewItem = GalleryPreviewItem(
            key = media.mediaKey,
            name = media.displayName,
            mime = media.mimeType.ifBlank {
                MediaPreviewActivity.guessMimeFromName(media.displayName)
            },
            localUri = media.uri.toString(),
            remoteId = remoteId,
            isVideo = media.isVideo,
        )

        fun fromCloud(entry: FileEntry, mime: String): GalleryPreviewItem = GalleryPreviewItem(
            key = entry.id,
            name = entry.name,
            mime = mime,
            remoteId = entry.id,
            isVideo = mime.startsWith("video/"),
        )
    }
}

object GalleryPreviewSession {
    @Volatile
    var items: List<GalleryPreviewItem> = emptyList()

    @Volatile
    var startIndex: Int = 0

    fun open(activity: Activity, items: List<GalleryPreviewItem>, index: Int) {
        if (items.isEmpty()) return
        this.items = items
        this.startIndex = index.coerceIn(0, items.lastIndex)
        activity.startActivity(Intent(activity, MediaPreviewActivity::class.java))
    }
}
