package net.neciparmagan.trdriver.data

import android.content.ContentUris
import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.Uri
import androidx.documentfile.provider.DocumentFile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext

data class FreeUpCandidate(
    val row: UploadedMediaRow,
    val uri: Uri,
)

data class FreeUpPlan(
    val candidates: List<FreeUpCandidate>,
    val alreadyGone: Int,
    val missingUri: Int,
)

data class FreeUpVerifyResult(
    val verified: List<FreeUpCandidate>,
    val remoteMissing: Int,
    val networkErrors: Int,
)

/**
 * Safe free-up: never delete local media unless the remote file is confirmed present.
 * Network switches / app death mid-run only pause; each file is re-verified before delete.
 */
object FreeUpSpace {
    suspend fun buildPlan(context: Context, db: UploadedMediaDb): FreeUpPlan = withContext(Dispatchers.IO) {
        val rows = db.listFreeable()
        val candidates = ArrayList<FreeUpCandidate>()
        var alreadyGone = 0
        var missingUri = 0
        for (row in rows) {
            val uri = resolveLocalUri(context, row)
            if (uri == null) {
                missingUri++
            } else if (!localExists(context, uri)) {
                db.markFreed(row.mediaKey)
                alreadyGone++
            } else {
                candidates += FreeUpCandidate(row, uri)
            }
        }
        FreeUpPlan(candidates, alreadyGone, missingUri)
    }

    /**
     * Verify each candidate on the server with retries (Wi‑Fi ↔ mobile safe).
     * Only returns items that are confirmed remote-present.
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
        var index = 0
        while (index < candidates.size) {
            val item = candidates[index]
            onProgress(index, candidates.size, item.row.mediaKey.takeLast(40))
            if (!hasNetwork(context)) {
                var waited = 0
                while (!hasNetwork(context) && waited < 20) {
                    delay(500)
                    waited++
                }
                if (!hasNetwork(context)) {
                    networkErrors += candidates.size - index
                    index = candidates.size
                } else {
                    // network recovered — retry same item
                }
            } else if (!localExists(context, item.uri)) {
                UploadedMediaDb(context).markFreed(item.row.mediaKey)
                index++
            } else {
                when (verifyRemoteWithRetry(api, item.row.remoteId)) {
                    true -> verified += item
                    false -> remoteMissing++
                    null -> networkErrors++
                }
                index++
            }
        }
        FreeUpVerifyResult(verified, remoteMissing, networkErrors)
    }

    private suspend fun verifyRemoteWithRetry(api: DriveApi, remoteId: String): Boolean? {
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
        // Legacy: mediaKey like content://media/.../id or volume/id
        val key = row.mediaKey
        if (key.startsWith("content://")) {
            return runCatching { Uri.parse(key) }.getOrNull()
        }
        // "$volume/$id" from MediaCatalog
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

    fun localExists(context: Context, uri: Uri): Boolean {
        return runCatching {
            when {
                uri.scheme == "content" -> {
                    context.contentResolver.openInputStream(uri)?.use { true } ?: false
                }
                uri.scheme == "file" -> {
                    val path = uri.path ?: return false
                    java.io.File(path).exists()
                }
                else -> {
                    DocumentFile.fromSingleUri(context, uri)?.exists() == true
                }
            }
        }.getOrDefault(false)
    }

    /** Direct delete for apps that own the URI / older APIs. Returns deleted keys. */
    fun deleteDirect(context: Context, items: List<FreeUpCandidate>): List<String> {
        val deleted = ArrayList<String>()
        val db = UploadedMediaDb(context)
        for (item in items) {
            val ok = runCatching {
                val rows = context.contentResolver.delete(item.uri, null, null)
                rows > 0 || !localExists(context, item.uri)
            }.getOrDefault(false)
            if (ok || !localExists(context, item.uri)) {
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
}
