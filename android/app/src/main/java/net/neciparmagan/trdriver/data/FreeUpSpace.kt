package net.neciparmagan.trdriver.data

import android.content.ContentUris
import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.Uri
import android.provider.MediaStore
import android.provider.OpenableColumns
import androidx.documentfile.provider.DocumentFile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import kotlin.math.abs
import kotlin.math.max

data class FreeUpCandidate(
    val row: UploadedMediaRow,
    val uri: Uri,
    val displayName: String = "",
)

data class FreeUpPlan(
    val candidates: List<FreeUpCandidate>,
    val alreadyGone: Int,
    val missingUri: Int,
    val sizeMismatch: Int,
    val presenceUnknown: Int,
)

data class FreeUpVerifyResult(
    val verified: List<FreeUpCandidate>,
    val remoteMissing: Int,
    val networkErrors: Int,
    val sizeMismatch: Int,
    val presenceUnknown: Int,
)

enum class LocalPresence {
    /** File is readable on device. */
    EXISTS,
    /** Confirmed missing (query returned empty / file gone). */
    GONE,
    /** Permission or I/O ambiguity — never treat as deleted. */
    UNKNOWN,
}

/**
 * Safe free-up: never delete local media unless:
 * 1) Row exists in UploadedMediaDb with non-empty remote_id
 * 2) Local URI still resolves and size still matches backup-time size
 * 3) Remote file still exists on server
 *
 * Never marks freed on permission failures. Never deletes cloud copies.
 */
object FreeUpSpace {
    suspend fun buildPlan(context: Context, db: UploadedMediaDb): FreeUpPlan = withContext(Dispatchers.IO) {
        val rows = db.listFreeable()
        val candidates = ArrayList<FreeUpCandidate>()
        var alreadyGone = 0
        var missingUri = 0
        var sizeMismatch = 0
        var presenceUnknown = 0
        for (row in rows) {
            if (row.remoteId.isBlank()) continue
            val uri = resolveLocalUri(context, row)
            if (uri == null) {
                missingUri++
                continue
            }
            when (localPresence(context, uri)) {
                LocalPresence.GONE -> {
                    db.markFreed(row.mediaKey)
                    alreadyGone++
                }
                LocalPresence.UNKNOWN -> presenceUnknown++
                LocalPresence.EXISTS -> {
                    if (!localSizeMatchesBackup(context, uri, row.sizeBytes)) {
                        sizeMismatch++
                    } else {
                        candidates += FreeUpCandidate(
                            row = row,
                            uri = uri,
                            displayName = resolveDisplayName(context, uri, row.mediaKey),
                        )
                    }
                }
            }
        }
        FreeUpPlan(candidates, alreadyGone, missingUri, sizeMismatch, presenceUnknown)
    }

    /**
     * Verify each candidate on the server with retries (Wi‑Fi ↔ mobile safe).
     * Only returns items that are confirmed remote-present AND still size-matched locally.
     */
    suspend fun verifyOnServer(
        context: Context,
        api: DriveApi,
        candidates: List<FreeUpCandidate>,
        onProgress: (done: Int, total: Int, name: String) -> Unit,
    ): FreeUpVerifyResult = withContext(Dispatchers.IO) {
        val verified = ArrayList<FreeUpCandidate>()
        var remoteMissing = 0
        var networkErrors = 0
        var sizeMismatch = 0
        var presenceUnknown = 0
        var index = 0
        while (index < candidates.size) {
            val item = candidates[index]
            onProgress(index, candidates.size, item.displayName.ifBlank { item.row.mediaKey.takeLast(40) })
            if (!hasNetwork(context)) {
                var waited = 0
                while (!hasNetwork(context) && waited < 20) {
                    delay(500)
                    waited++
                }
                if (!hasNetwork(context)) {
                    networkErrors += candidates.size - index
                    index = candidates.size
                    continue
                }
            }
            when (localPresence(context, item.uri)) {
                LocalPresence.GONE -> {
                    UploadedMediaDb(context).markFreed(item.row.mediaKey)
                    index++
                    continue
                }
                LocalPresence.UNKNOWN -> {
                    presenceUnknown++
                    index++
                    continue
                }
                LocalPresence.EXISTS -> Unit
            }
            if (!localSizeMatchesBackup(context, item.uri, item.row.sizeBytes)) {
                sizeMismatch++
                index++
                continue
            }
            when (verifyRemoteWithRetry(api, item.row.remoteId)) {
                true -> verified += item
                false -> remoteMissing++
                null -> networkErrors++
            }
            index++
        }
        FreeUpVerifyResult(verified, remoteMissing, networkErrors, sizeMismatch, presenceUnknown)
    }

    /** Final gate immediately before launching the system delete UI. */
    suspend fun recheckBeforeDelete(
        context: Context,
        api: DriveApi,
        items: List<FreeUpCandidate>,
    ): List<FreeUpCandidate> = withContext(Dispatchers.IO) {
        val out = ArrayList<FreeUpCandidate>()
        for (item in items) {
            if (item.row.remoteId.isBlank()) continue
            if (localPresence(context, item.uri) != LocalPresence.EXISTS) continue
            if (!localSizeMatchesBackup(context, item.uri, item.row.sizeBytes)) continue
            when (verifyRemoteWithRetry(api, item.row.remoteId)) {
                true -> out += item
                else -> Unit
            }
        }
        out
    }

    private suspend fun verifyRemoteWithRetry(api: DriveApi, remoteId: String): Boolean? {
        if (remoteId.isBlank()) return false
        var lastNetwork: Throwable? = null
        repeat(4) { attempt ->
            try {
                return api.remoteFileExists(remoteId)
            } catch (e: Exception) {
                lastNetwork = e
                delay(400L * (attempt + 1))
            }
        }
        return if (lastNetwork != null) null else false
    }

    fun resolveLocalUri(@Suppress("UNUSED_PARAMETER") context: Context, row: UploadedMediaRow): Uri? {
        if (row.localUri.isNotBlank()) {
            return runCatching { Uri.parse(row.localUri) }.getOrNull()
        }
        val key = row.mediaKey
        if (key.startsWith("content://")) {
            return runCatching { Uri.parse(key) }.getOrNull()
        }
        val slash = key.lastIndexOf('/')
        if (slash > 0) {
            val id = key.substring(slash + 1).toLongOrNull() ?: return null
            val volume = key.substring(0, slash)
            return runCatching {
                ContentUris.withAppendedId(Uri.parse(volume), id)
            }.getOrNull()
        }
        if (key.startsWith("saf:")) {
            return runCatching { Uri.parse(key.removePrefix("saf:")) }.getOrNull()
        }
        return null
    }

    fun localPresence(context: Context, uri: Uri): LocalPresence {
        return try {
            when {
                uri.scheme.equals("content", ignoreCase = true) -> {
                    context.contentResolver.query(
                        uri,
                        arrayOf(MediaStore.MediaColumns._ID),
                        null,
                        null,
                        null,
                    )?.use { c ->
                        if (c.moveToFirst()) LocalPresence.EXISTS else LocalPresence.GONE
                    } ?: run {
                        // Non-MediaStore content provider
                        context.contentResolver.openAssetFileDescriptor(uri, "r")?.use {
                            LocalPresence.EXISTS
                        } ?: LocalPresence.GONE
                    }
                }
                uri.scheme.equals("file", ignoreCase = true) -> {
                    val path = uri.path ?: return LocalPresence.GONE
                    if (java.io.File(path).exists()) LocalPresence.EXISTS else LocalPresence.GONE
                }
                else -> {
                    when (DocumentFile.fromSingleUri(context, uri)?.exists()) {
                        true -> LocalPresence.EXISTS
                        false -> LocalPresence.GONE
                        null -> LocalPresence.UNKNOWN
                    }
                }
            }
        } catch (_: SecurityException) {
            LocalPresence.UNKNOWN
        } catch (_: Exception) {
            LocalPresence.UNKNOWN
        }
    }

    fun localExists(context: Context, uri: Uri): Boolean =
        localPresence(context, uri) == LocalPresence.EXISTS

    /**
     * Guards against MediaStore ID reuse: local byte size must still match
     * the size recorded when backup succeeded.
     */
    fun localSizeMatchesBackup(context: Context, uri: Uri, expectedBytes: Long): Boolean {
        if (expectedBytes <= 0L) return false
        val actual = MediaAccess.resolveContentLength(context, uri, -1L)
        if (actual <= 0L) return false
        val slack = max(4096L, expectedBytes / 500L) // ~0.2% or 4 KB
        return abs(actual - expectedBytes) <= slack
    }

    fun resolveDisplayName(context: Context, uri: Uri, fallback: String): String {
        runCatching {
            context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
                if (c.moveToFirst()) {
                    val i = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    if (i >= 0) c.getString(i)?.takeIf { it.isNotBlank() }?.let { return it }
                }
            }
        }
        return fallback.substringAfterLast('/').ifBlank { fallback }
    }

    /** Direct delete for apps that own the URI / older APIs. Returns deleted keys. */
    fun deleteDirect(context: Context, items: List<FreeUpCandidate>): List<String> {
        val deleted = ArrayList<String>()
        val db = UploadedMediaDb(context)
        for (item in items) {
            if (item.row.remoteId.isBlank()) continue
            if (localPresence(context, item.uri) != LocalPresence.EXISTS) continue
            if (!localSizeMatchesBackup(context, item.uri, item.row.sizeBytes)) continue
            val ok = runCatching {
                val rows = context.contentResolver.delete(item.uri, null, null)
                rows > 0 || localPresence(context, item.uri) == LocalPresence.GONE
            }.getOrDefault(false)
            if (ok || localPresence(context, item.uri) == LocalPresence.GONE) {
                db.markFreed(item.row.mediaKey)
                deleted += item.row.mediaKey
            }
        }
        return deleted
    }

    fun hasNetwork(context: Context): Boolean {
        val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val net = cm.activeNetwork ?: return false
        val caps = cm.getNetworkCapabilities(net) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
    }

    fun formatBytes(bytes: Long): String = SessionStore.formatBytes(bytes)

    fun previewNames(items: List<FreeUpCandidate>, limit: Int = 12): String {
        if (items.isEmpty()) return ""
        val names = items.take(limit).map { it.displayName.ifBlank { it.row.mediaKey.takeLast(24) } }
        val more = items.size - names.size
        return buildString {
            names.forEachIndexed { i, n ->
                append("• ")
                append(n)
                if (i < names.lastIndex || more > 0) append('\n')
            }
            if (more > 0) append("… ve $more dosya daha")
        }
    }
}
