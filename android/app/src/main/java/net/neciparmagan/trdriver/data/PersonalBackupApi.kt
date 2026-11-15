package net.neciparmagan.trdriver.data

import android.net.Uri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.concurrent.TimeUnit

/**
 * Client for the `/api/personal-backup` endpoints (Bearer device token).
 *
 * Requests are small JSON batches, so timeouts are far shorter than the media
 * upload client. The existing media bandwidth limit is untouched: this client
 * only ever sends a few hundred kilobytes per batch.
 */
class PersonalBackupApi(private val session: SessionStore) {
    private val json = PersonalBackupCodec.json

    private val http = OkHttpClient.Builder()
        .connectTimeout(20, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .writeTimeout(60, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    private fun base() = session.serverUrl.trimEnd('/')

    private fun authed(url: String): Request.Builder {
        val builder = Request.Builder().url(url)
        val token = session.token
        if (!token.isNullOrBlank()) {
            builder.header("Authorization", "Bearer $token")
        }
        return builder
    }

    private fun parseError(body: String?): String {
        if (body.isNullOrBlank()) return "İstek başarısız"
        return runCatching {
            json.decodeFromString(ApiError.serializer(), body).error.ifBlank { body }
        }.getOrDefault(body.take(200))
    }

    suspend fun sync(kind: String, request: PersonalBackupSyncRequest): PersonalBackupSyncResult =
        withContext(Dispatchers.IO) {
            val payload = json.encodeToString(PersonalBackupSyncRequest.serializer(), request)
            val req = authed("${base()}/api/personal-backup/${Uri.encode(kind)}")
                .post(payload.toRequestBody(JSON))
                .build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) {
                    throw HttpStatusIOException(
                        code = resp.code,
                        message = parseError(text),
                        retryAfterSec = resp.header("Retry-After")?.toIntOrNull() ?: 0,
                    )
                }
                json.decodeFromString(PersonalBackupSyncResult.serializer(), text)
            }
        }

    /** Restore side: one page of decrypted records from the server. */
    suspend fun list(
        kind: String,
        since: String? = null,
        after: String? = null,
        limit: Int = 200,
    ): PersonalBackupListPage = withContext(Dispatchers.IO) {
        val url = buildString {
            append(base())
            append("/api/personal-backup/")
            append(Uri.encode(kind))
            val params = buildList {
                if (!since.isNullOrBlank()) add("since=${Uri.encode(since)}")
                if (!after.isNullOrBlank()) add("after=${Uri.encode(after)}")
                add("limit=${limit.coerceIn(1, 500)}")
            }
            append("?")
            append(params.joinToString("&"))
        }
        val req = authed(url).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) {
                throw HttpStatusIOException(resp.code, parseError(text))
            }
            json.decodeFromString(PersonalBackupListPage.serializer(), text)
        }
    }

    suspend fun status(): PersonalBackupStatus = withContext(Dispatchers.IO) {
        val req = authed("${base()}/api/personal-backup/status").get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) {
                throw HttpStatusIOException(resp.code, parseError(text))
            }
            json.decodeFromString(PersonalBackupStatus.serializer(), text)
        }
    }

    /** Erase the server copy of one kind (used by "sunucudaki yedeği sil"). */
    suspend fun purge(kind: String) = withContext(Dispatchers.IO) {
        val req = authed("${base()}/api/personal-backup/${Uri.encode(kind)}").delete().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) {
                throw HttpStatusIOException(resp.code, parseError(text))
            }
        }
    }

    private companion object {
        val JSON = "application/json; charset=utf-8".toMediaType()
    }
}
