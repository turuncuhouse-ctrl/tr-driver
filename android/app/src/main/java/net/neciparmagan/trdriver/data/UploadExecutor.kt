package net.neciparmagan.trdriver.data

import android.content.Context
import android.net.Uri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Resolves name conflicts and applies network policy before calling [DriveApi].
 */
object UploadExecutor {
    suspend fun uploadUri(
        context: Context,
        api: DriveApi,
        session: SessionStore,
        parentId: String,
        uri: Uri,
        displayName: String,
        mimeType: String = "",
        sizeHint: Long = -1L,
        conflict: UploadConflictPolicy = UploadConflictPolicy.ASK,
        onProgress: ((Long, Long) -> Unit)? = null,
        onRetry: ((Int, Throwable) -> Unit)? = null,
        onConflictAsk: (suspend (existingName: String) -> UploadConflictPolicy)? = null,
    ): FileEntry = withContext(Dispatchers.IO) {
        val resolvedSize = MediaAccess.resolveContentLength(context, uri, sizeHint)
        // Manual / in-app uploads: mobile data OK
        UploadNetworkGate.awaitUploadAllowed(context, session, resolvedSize, wifiOnly = false)
        val policy = resolveConflictPolicy(
            api = api,
            parentId = parentId,
            fileName = displayName,
            requested = conflict,
            onConflictAsk = onConflictAsk,
        )
        if (policy == UploadConflictPolicy.SKIP) throw SkipUploadException()
        try {
            val attempts = retryAttemptsForSize(resolvedSize)
            UploadRetry.run(
                context = context,
                attempts = attempts,
                fileBytes = resolvedSize,
                wifiOnly = false,
                onRetry = onRetry,
            ) {
                UploadNetworkGate.awaitUploadAllowed(context, session, resolvedSize, wifiOnly = false)
                api.upload(
                    parentId = parentId,
                    uri = uri,
                    displayName = displayName,
                    conflict = serverConflict(policy),
                    onProgress = onProgress,
                    onRetry = onRetry,
                )
            }
        } finally {
            UploadNetworkGate.unbindUploadNetwork(context)
        }
    }

    suspend fun uploadMediaItem(
        context: Context,
        api: DriveApi,
        session: SessionStore,
        parentId: String,
        media: LocalMedia,
        conflict: UploadConflictPolicy = UploadConflictPolicy.RENAME,
        onProgress: ((Long, Long) -> Unit)? = null,
        onRetry: ((Int, Throwable) -> Unit)? = null,
    ): FileEntry = withContext(Dispatchers.IO) {
        val size = MediaAccess.resolveContentLength(context, media.uri, media.sizeBytes)
        // Gallery backup: Wi‑Fi only
        UploadNetworkGate.awaitUploadAllowed(context, session, size, wifiOnly = true)
        val policy = resolveConflictPolicy(api, parentId, media.displayName, conflict, null)
        if (policy == UploadConflictPolicy.SKIP) throw SkipUploadException()
        val network = UploadNetworkGate.bindUploadNetwork(context, wifiOnly = true)
        val previousBackupMode = UploadBandwidthLimiter.backupMode
        UploadBandwidthLimiter.backupMode = true
        try {
            UploadRetry.run(
                context = context,
                attempts = retryAttemptsForSize(size),
                fileBytes = size,
                wifiOnly = true,
                onRetry = onRetry,
            ) {
                UploadNetworkGate.awaitUploadAllowed(context, session, size, wifiOnly = true)
                // Large backup files use the resumable API so an interrupted
                // transfer continues from the server-side offset. Small files
                // (most photos after prep) keep the proven multipart path.
                if (policy == UploadConflictPolicy.RENAME && size >= BackupPolicy.RESUMABLE_MIN_BYTES) {
                    try {
                        api.uploadMediaResumable(parentId, media, onProgress)
                    } catch (e: kotlinx.coroutines.CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        if (!shouldFallbackToMultipart(e)) throw e
                        api.uploadMedia(
                            parentId = parentId,
                            media = media,
                            conflict = serverConflict(policy),
                            onProgress = onProgress,
                            onRetry = onRetry,
                        )
                    }
                } else {
                    api.uploadMedia(
                        parentId = parentId,
                        media = media,
                        conflict = serverConflict(policy),
                        onProgress = onProgress,
                        onRetry = onRetry,
                    )
                }
            }
        } finally {
            UploadBandwidthLimiter.backupMode = previousBackupMode
            UploadNetworkGate.unbindUploadNetwork(context)
        }
    }

    /**
     * Protocol-level failures of the resumable API (bad session, name taken,
     * wrong payload) are recovered by falling back to multipart. Network and
     * server-load errors are NOT fallbacks: UploadRetry resumes the chunked
     * session from its saved offset instead of restarting the file.
     */
    private fun shouldFallbackToMultipart(error: Throwable): Boolean {
        if (error is SkipUploadException ||
            error is DuplicateNameException ||
            error is UploadNetworkBlockedException ||
            error is UploadOffsetMismatchException
        ) {
            return false
        }
        if (error is HttpStatusIOException && (error.code == 429 || error.code in 500..599)) {
            return false
        }
        val msg = error.message?.lowercase().orEmpty()
        if (msg.contains("quota")) return false
        if (error is HttpStatusIOException && error.code == 400 && msg.isBlank()) return false
        return msg.contains("file name conflict") ||
            msg.contains("upload session") ||
            msg.contains("content hash mismatch") ||
            msg.contains("chunk must be") ||
            (error is HttpStatusIOException && error.code == 400)
    }

    private suspend fun resolveConflictPolicy(
        api: DriveApi,
        parentId: String,
        fileName: String,
        requested: UploadConflictPolicy,
        onConflictAsk: (suspend (String) -> UploadConflictPolicy)?,
    ): UploadConflictPolicy {
        if (requested != UploadConflictPolicy.ASK) return requested
        val existing = api.findExistingFile(parentId, fileName) ?: return UploadConflictPolicy.RENAME
        val chosen = onConflictAsk?.invoke(existing.name)
        return chosen ?: UploadConflictPolicy.RENAME
    }

    private fun serverConflict(policy: UploadConflictPolicy): String = when (policy) {
        UploadConflictPolicy.OVERWRITE -> "overwrite"
        UploadConflictPolicy.RENAME -> "rename"
        UploadConflictPolicy.SKIP -> "skip"
        UploadConflictPolicy.ASK -> "rename"
    }

    private fun retryAttemptsForSize(bytes: Long): Int {
        if (bytes <= 0) return 8
        val mb = bytes / (1024 * 1024)
        return (6 + (mb / 5).toInt()).coerceIn(6, 14)
    }
}
