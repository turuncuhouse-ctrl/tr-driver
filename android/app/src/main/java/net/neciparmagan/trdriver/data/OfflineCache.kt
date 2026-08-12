package net.neciparmagan.trdriver.data

import android.content.Context
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import java.io.File

/** Simple offline cache: last folder listings + downloaded files. */
class OfflineCache(context: Context) {
    private val root = File(context.cacheDir, "offline").also { it.mkdirs() }
    private val downloads = File(context.cacheDir, "downloads").also { it.mkdirs() }
    private val json = Json { ignoreUnknownKeys = true; isLenient = true }

    fun saveListing(parentId: String?, files: List<FileEntry>) {
        val key = (parentId ?: "root").replace(Regex("[^a-zA-Z0-9_-]"), "_")
        val file = File(root, "list-$key.json")
        runCatching {
            file.writeText(json.encodeToString(ListSerializer(FileEntry.serializer()), files))
        }
    }

    fun loadListing(parentId: String?): List<FileEntry>? {
        val key = (parentId ?: "root").replace(Regex("[^a-zA-Z0-9_-]"), "_")
        val file = File(root, "list-$key.json")
        if (!file.exists()) return null
        return runCatching {
            json.decodeFromString(ListSerializer(FileEntry.serializer()), file.readText())
        }.getOrNull()
    }

    fun listDownloaded(): List<File> {
        return downloads.listFiles()?.filter { it.isFile && it.length() > 0 }?.sortedByDescending { it.lastModified() }
            ?: emptyList()
    }

    fun downloadsDir(): File = downloads
}
