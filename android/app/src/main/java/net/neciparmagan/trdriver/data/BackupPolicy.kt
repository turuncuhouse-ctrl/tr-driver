package net.neciparmagan.trdriver.data

/**
 * Pure backup rules: file classification, per-type toggles, size limits,
 * safe names/paths and the standard folder presets.
 *
 * Framework-free on purpose so every rule is covered by JVM unit tests.
 */
object BackupPolicy {
    const val KIND_PHOTO = "photo"
    const val KIND_VIDEO = "video"
    const val KIND_AUDIO = "audio"
    const val KIND_DOC = "doc"

    /** Files larger than this are skipped (server single-file cap is 10 GiB). */
    const val MAX_FILE_BYTES = 8L * 1024 * 1024 * 1024

    /** Update APK downloads are capped far above any real release size. */
    const val MAX_APK_BYTES = 512L * 1024 * 1024

    /** At/above this size backup uses the resumable (chunked) upload API. */
    const val RESUMABLE_MIN_BYTES = 4L * 1024 * 1024

    /** Which file types the user allows to be backed up. */
    data class TypeToggles(
        val photo: Boolean,
        val video: Boolean,
        val audio: Boolean,
        val doc: Boolean,
    ) {
        fun allows(kind: String): Boolean = when (kind) {
            KIND_PHOTO -> photo
            KIND_VIDEO -> video
            KIND_AUDIO -> audio
            else -> doc
        }
    }

    /** Classification by MIME first, extension second; everything else = doc. */
    fun kindOf(mimeType: String?, displayName: String): String {
        val mime = mimeType?.substringBefore(';')?.trim()?.lowercase().orEmpty()
        if (mime.startsWith("image/")) return KIND_PHOTO
        if (mime.startsWith("video/")) return KIND_VIDEO
        if (mime.startsWith("audio/")) return KIND_AUDIO
        return when (extensionOf(displayName)) {
            in PHOTO_EXTS -> KIND_PHOTO
            in VIDEO_EXTS -> KIND_VIDEO
            in AUDIO_EXTS -> KIND_AUDIO
            else -> KIND_DOC
        }
    }

    fun extensionOf(name: String): String =
        name.substringAfterLast('.', "")
            .lowercase()
            .filter { it in 'a'..'z' || it in '0'..'9' }

    /** Empty/unknown sizes and oversized files are never eligible. */
    fun allowsSize(bytes: Long): Boolean = bytes in 1L..MAX_FILE_BYTES

    /**
     * Single path segment for uploads: strips separators, control characters
     * and traversal names so a hostile file name can never escape its folder.
     */
    fun sanitizeDisplayName(raw: String): String {
        val cleaned = raw
            .replace(Regex("[\\u0000-\\u001f]"), "_")
            .replace(Regex("[\\\\/:*?\"<>|]"), "_")
            .trim()
            .take(150)
            .trim()
        return when {
            cleaned.isBlank() -> "dosya"
            cleaned.all { it == '.' } -> "dosya"
            else -> cleaned
        }
    }

    private val SAFE_PATH_PREFIXES = listOf(
        "/storage/emulated/0",
        "/sdcard",
        "/mnt/sdcard",
        "/storage/self/primary",
    )

    private val SECONDARY_VOLUME = Regex("^/storage/[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}(/.*)?$")

    /**
     * Only absolute shared-storage paths may be walked directly: never
     * relative paths, never `..`, never app-private or system roots.
     */
    fun isSafeDirectPath(path: String): Boolean {
        val raw = path.trim()
        if (raw.isEmpty() || raw.contains('\u0000')) return false
        if (!raw.startsWith("/")) return false
        if (raw.contains("..")) return false
        val normalized = raw.trimEnd('/')
        if (normalized.isBlank()) return false
        if (SAFE_PATH_PREFIXES.any { normalized == it || normalized.startsWith("$it/") }) return true
        return SECONDARY_VOLUME.matches(normalized)
    }

    /** Server ids (batch/session/file) are UUIDs; anything else is rejected. */
    fun isSafeServerId(id: String): Boolean =
        Regex("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
            .matches(id)

    /**
     * Maps a SAF tree URI (or its document id) to an absolute primary-volume
     * path, so a tree already covered by a directly-walked preset folder is
     * not scanned (and uploaded) twice. Secondary volumes return null — those
     * trees keep using SAF.
     */
    fun treeDocumentIdToPath(docId: String): String? {
        if (!docId.startsWith("primary:")) return null
        val rest = docId.removePrefix("primary:")
        if (rest.isEmpty() || rest.contains("..")) return null
        val rel = rest.trim('/')
        return if (rel.isEmpty()) "/storage/emulated/0" else "/storage/emulated/0/$rel"
    }

    fun treeUriToPath(treeUri: String): String? {
        val marker = "/tree/"
        val idx = treeUri.indexOf(marker)
        if (idx < 0) return null
        val encoded = treeUri
            .substring(idx + marker.length)
            .substringBefore('/')
            .substringBefore('?')
        if (encoded.isEmpty()) return null
        val docId = try {
            java.net.URLDecoder.decode(encoded, "UTF-8")
        } catch (e: Exception) {
            return null
        }
        return treeDocumentIdToPath(docId)
    }

    /** True when [treePath] is the same folder as, or inside, [presetPath]. */
    fun pathCoveredByPreset(treePath: String?, presetPaths: List<String>): Boolean {
        if (treePath.isNullOrBlank()) return false
        val tree = treePath.trimEnd('/')
        for (raw in presetPaths) {
            val preset = raw.trimEnd('/')
            if (tree == preset || tree.startsWith("$preset/")) return true
        }
        return false
    }

    /** One standard backup folder the user can add with a single tap. */
    data class FolderPreset(
        val label: String,
        val path: String,
        val treeDocumentId: String,
    )

    fun presets(): List<FolderPreset> = listOf(
        FolderPreset("Download", "/storage/emulated/0/Download", "primary:Download"),
        FolderPreset("DCIM", "/storage/emulated/0/DCIM", "primary:DCIM"),
        FolderPreset("Pictures", "/storage/emulated/0/Pictures", "primary:Pictures"),
        FolderPreset("Documents", "/storage/emulated/0/Documents", "primary:Documents"),
        FolderPreset("Music", "/storage/emulated/0/Music", "primary:Music"),
        FolderPreset("Movies", "/storage/emulated/0/Movies", "primary:Movies"),
        FolderPreset(
            "WhatsApp Medya",
            "/storage/emulated/0/Android/media/com.whatsapp/WhatsApp/Media",
            "primary:Android/media/com.whatsapp/WhatsApp/Media",
        ),
        FolderPreset(
            "WhatsApp Yedek",
            "/storage/emulated/0/Android/media/com.whatsapp/WhatsApp",
            "primary:Android/media/com.whatsapp/WhatsApp",
        ),
    )

    private val PHOTO_EXTS = setOf(
        "jpg", "jpeg", "png", "webp", "gif", "bmp", "heic", "heif", "raw", "dng", "tif", "tiff",
    )
    private val VIDEO_EXTS = setOf(
        "mp4", "mkv", "avi", "mov", "m4v", "3gp", "webm", "flv", "wmv", "mpg", "mpeg",
    )
    private val AUDIO_EXTS = setOf(
        "mp3", "m4a", "aac", "wav", "flac", "ogg", "oga", "opus", "wma", "amr", "mid", "midi",
    )
}
