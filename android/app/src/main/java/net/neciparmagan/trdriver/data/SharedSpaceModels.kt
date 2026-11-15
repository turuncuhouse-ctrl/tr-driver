package net.neciparmagan.trdriver.data

import kotlinx.serialization.Serializable

/** Models for the server-wide "Ortak Paylaşım" folder (`/api/shared/`). */

@Serializable
data class SharedEntry(
    val name: String = "",
    val path: String = "",
    val isDir: Boolean = false,
    val size: Long = 0,
    val modifiedAt: String = "",
    /** none | read | write | admin — what the signed-in user may do with this entry. */
    val level: String = "none",
)

@Serializable
data class SharedListing(
    val path: String = "",
    val level: String = "none",
    val entries: List<SharedEntry>? = null,
)

@Serializable
data class SharedAccess(
    val available: Boolean = false,
    val rootLevel: String = "none",
    val maxLevel: String = "none",
    val admin: Boolean = false,
)

@Serializable
data class SharedQuota(
    val quotaBytes: Long = 0,
    val limitBytes: Long = 0,
    val usedBytes: Long = 0,
    val freeBytes: Long = 0,
    val usedPercent: Int = 0,
    /** ok | warning | full */
    val level: String = "ok",
    val message: String = "",
)

@Serializable
data class SharedStatus(
    val access: SharedAccess = SharedAccess(),
    val quota: SharedQuota? = null,
    val unavailable: Boolean = false,
)

/** Body of the folder and rename requests. No defaults: both fields are always sent. */
@Serializable
data class SharedPathRequest(
    val path: String,
    val name: String,
)

/** Body of the delete request. The server rejects unknown fields, so it carries the path only. */
@Serializable
data class SharedDeleteRequest(
    val path: String,
)

/**
 * Path arithmetic for the shared folder. Paths are relative to its root, use
 * "/" as separator, and the root itself is the empty string. The server
 * validates everything again; these helpers only keep the client from sending
 * requests that are bound to be refused.
 */
object SharedSpacePaths {
    fun join(dir: String, name: String): String = if (dir.isEmpty()) name else "$dir/$name"

    fun parent(path: String): String {
        val cut = path.lastIndexOf('/')
        return if (cut < 0) "" else path.substring(0, cut)
    }

    fun nameOf(path: String): String = path.substringAfterLast('/')

    fun canWrite(level: String): Boolean = level == "write" || level == "admin"

    /** A single file or folder name the server will accept. */
    fun isValidName(name: String): Boolean {
        if (name.isBlank() || name == "." || name == "..") return false
        if (name.length > 255) return false
        return name.none { it == '/' || it == '\\' || it.code < 0x20 || it.code == 0x7f }
    }

    /** Name for the local cache copy of a download: never a path. */
    fun cacheFileName(name: String): String {
        val cleaned = name
            .replace('/', '_')
            .replace('\\', '_')
            .replace('\u0000', '_')
            .trim()
        return if (cleaned.isEmpty() || cleaned == "." || cleaned == "..") "indirilen.bin" else cleaned
    }

    fun formatSize(bytes: Long): String {
        if (bytes <= 0) return "0 B"
        val units = arrayOf("B", "KB", "MB", "GB", "TB")
        var value = bytes.toDouble()
        var index = 0
        while (value >= 1024 && index < units.size - 1) {
            value /= 1024
            index++
        }
        return if (index == 0) "${bytes} B" else String.format(java.util.Locale.US, "%.1f %s", value, units[index])
    }
}
