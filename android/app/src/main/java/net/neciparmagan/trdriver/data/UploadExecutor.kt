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
        UploadNetworkGate.awaitUploadAllowed(context, session, resolvedSize)
        val policy = resolveConflictPolicy(
            api = api,
            parentId = parentId,
            fileName = displayName,
            requested = conflict,
            onConflictAsk = onConflictAsk,
        )
        if (policy == UploadConflictPolicy.SKIP) throw SkipUploadException()
        val network = UploadNetworkGate.bindUploadNetwork(context)
        try {
            val attempts = retryAttemptsForSize(resolvedSize)
            UploadRetry.run(
                context = context,
                attempts = attempts,
                fileBytes = resolvedSize,
                onRetry = onRetry,
            ) {
                UploadNetworkGate.awaitUploadAllowed(context, session, resolvedSize)
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
        UploadNetworkGate.awaitUploadAllowed(context, session, size)
        val policy = resolveConflictPolicy(api, parentId, media.displayName, conflict, null)
        if (policy == UploadConflictPolicy.SKIP) throw SkipUploadException()
        val network = UploadNetworkGate.bindUploadNetwork(context)
        try {
            UploadRetry.run(
                context = context,
                attempts = retryAttemptsForSize(size),
                fileBytes = size,
                onRetry = onRetry,
            ) {
                UploadNetworkGate.awaitUploadAllowed(context, session, size)
                api.uploadMedia(
                    parentId = parentId,
                    media = media,
                    conflict = serverConflict(policy),
                    onProgress = onProgress,
                    onRetry = onRetry,
                )
            }
        } finally {
            UploadNetworkGate.unbindUploadNetwork(context)
        }
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
