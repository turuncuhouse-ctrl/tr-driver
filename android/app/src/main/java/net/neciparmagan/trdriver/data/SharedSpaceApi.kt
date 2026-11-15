package net.neciparmagan.trdriver.data

import android.content.Context
import android.net.Uri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okio.BufferedSink
import okio.source
import java.io.File
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * Client for the server-wide shared folder ("Ortak Paylaşım", `/api/shared/`),
 * authenticated with the device's Bearer token. What the user may see and do
 * is decided by the server on every request; the levels returned in listings
 * only tell the UI which actions are worth offering.
 */
class SharedSpaceApi(private val session: SessionStore, private val appContext: Context) {
    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
    }

    private val http = OkHttpClient.Builder()
        .connectTimeout(30, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.MINUTES)
        .writeTimeout(30, TimeUnit.MINUTES)
        .retryOnConnectionFailure(true)
        .build()

    private fun url(endpoint: String, vararg query: Pair<String, String>): HttpUrl {
        val builder = (session.serverUrl.trimEnd('/') + "/api/shared/" + endpoint).toHttpUrl().newBuilder()
        for ((key, value) in query) {
            builder.addQueryParameter(key, value)
        }
        return builder.build()
    }

    private fun authed(url: HttpUrl): Request.Builder {
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
            json.decodeFromString(ApiError.serializer(), body).error.ifBlank { body.take(200) }
        }.getOrDefault(body.take(200))
    }

    private fun fail(code: Int, body: String?, retryAfter: String?): Nothing {
        throw HttpStatusIOException(
            code = code,
            message = parseError(body),
            retryAfterSec = retryAfter?.toIntOrNull() ?: 0,
        )
    }

    suspend fun status(): SharedStatus = withContext(Dispatchers.IO) {
        val req = authed(url("status")).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) fail(resp.code, text, resp.header("Retry-After"))
            json.decodeFromString(SharedStatus.serializer(), text)
        }
    }

    suspend fun list(path: String): SharedListing = withContext(Dispatchers.IO) {
        val req = authed(url("list", "path" to path)).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) fail(resp.code, text, resp.header("Retry-After"))
            json.decodeFromString(SharedListing.serializer(), text)
        }
    }

    private fun postJson(endpoint: String, payload: String) {
        val req = authed(url(endpoint)).post(payload.toRequestBody(JSON)).build()
        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) fail(resp.code, resp.body?.string(), resp.header("Retry-After"))
        }
    }

    suspend fun createFolder(dir: String, name: String) = withContext(Dispatchers.IO) {
        postJson("folder", json.encodeToString(SharedPathRequest.serializer(), SharedPathRequest(path = dir, name = name)))
    }

    suspend fun rename(path: String, newName: String) = withContext(Dispatchers.IO) {
        postJson("rename", json.encodeToString(SharedPathRequest.serializer(), SharedPathRequest(path = path, name = newName)))
    }

    suspend fun delete(path: String) = withContext(Dispatchers.IO) {
        postJson("delete", json.encodeToString(SharedDeleteRequest.serializer(), SharedDeleteRequest(path = path)))
    }

    /** Downloads a file into the app's private cache and returns it. */
    suspend fun downloadToCache(entry: SharedEntry): File = withContext(Dispatchers.IO) {
        val dir = File(appContext.cacheDir, "downloads").also { it.mkdirs() }
        val dest = File(dir, SharedSpacePaths.cacheFileName(entry.name))
        val req = authed(url("download", "path" to entry.path)).get().build()
        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) fail(resp.code, resp.body?.string(), resp.header("Retry-After"))
            val stream = resp.body?.byteStream() ?: throw IOException("Boş yanıt")
            stream.use { input ->
                dest.outputStream().use { output -> input.copyTo(output) }
            }
        }
        dest
    }

    /**
     * Uploads the content behind [uri] into [dir] as [name]. The server answers
     * 409 when the name is taken; call again with [overwrite] once the user
     * agreed to replace it.
     */
    suspend fun upload(dir: String, name: String, uri: Uri, size: Long, overwrite: Boolean) = withContext(Dispatchers.IO) {
        val resolver = appContext.contentResolver
        val body = object : RequestBody() {
            override fun contentType(): MediaType = OCTET_STREAM
            override fun contentLength(): Long = if (size >= 0) size else -1L
            override fun isOneShot(): Boolean = true
            override fun writeTo(sink: BufferedSink) {
                val input = resolver.openInputStream(uri) ?: throw IOException("Dosya açılamadı")
                input.use { stream -> sink.writeAll(stream.source()) }
            }
        }
        val target = if (overwrite) {
            url("upload", "path" to dir, "name" to name, "overwrite" to "1")
        } else {
            url("upload", "path" to dir, "name" to name)
        }
        val req = authed(target).post(body).build()
        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) fail(resp.code, resp.body?.string(), resp.header("Retry-After"))
        }
    }

    private companion object {
        val JSON: MediaType = "application/json; charset=utf-8".toMediaType()
        val OCTET_STREAM: MediaType = "application/octet-stream".toMediaType()
    }
}
