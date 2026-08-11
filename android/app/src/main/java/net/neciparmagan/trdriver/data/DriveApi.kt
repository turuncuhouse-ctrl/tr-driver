package net.neciparmagan.trdriver.data

import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.OpenableColumns
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import okhttp3.MediaType
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.MultipartBody
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okio.BufferedSink
import okio.source
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit

class DriveApi(private val session: SessionStore, private val appContext: Context) {
    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
    }

    private val http = OkHttpClient.Builder()
        .connectTimeout(30, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.MINUTES)
        .writeTimeout(30, TimeUnit.MINUTES)
        .build()

    private fun base() = session.serverUrl.trimEnd('/')

    private fun authed(builder: Request.Builder): Request.Builder {
        val token = session.token
        if (!token.isNullOrBlank()) {
            builder.header("Authorization", "Bearer $token")
        }
        return builder
    }

    private fun parseError(body: String?): String {
        if (body.isNullOrBlank()) return "İstek başarısız"
        return try {
            json.decodeFromString(ApiError.serializer(), body).error.ifBlank { body }
        } catch (_: Exception) {
            body.take(300)
        }
    }

    suspend fun login(email: String, password: String, challengeToken: String? = null, code: String? = null): DeviceLoginResponse =
        withContext(Dispatchers.IO) {
            val deviceName = "Android-" + (Build.MODEL ?: "Phone")
            val payload = DeviceLoginRequest(
                email = email,
                password = password,
                deviceName = deviceName,
                challengeToken = challengeToken,
                code = code,
            )
            val body = json.encodeToString(DeviceLoginRequest.serializer(), payload)
                .toRequestBody("application/json".toMediaType())
            val req = Request.Builder()
                .url("${base()}/api/auth/device-login")
                .post(body)
                .build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) {
                    throw IOException(parseError(text))
                }
                val parsed = json.decodeFromString(DeviceLoginResponse.serializer(), text)
                if (!parsed.requires2FA && !parsed.token.isNullOrBlank()) {
                    session.token = parsed.token
                    session.email = email
                    session.storageRootId = parsed.user?.storageRootId.orEmpty()
                }
                parsed
            }
        }

    suspend fun me(): User = withContext(Dispatchers.IO) {
        val req = authed(Request.Builder().url("${base()}/api/auth/me")).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            val user = json.decodeFromString(User.serializer(), text)
            if (user.storageRootId.isNotBlank()) {
                session.storageRootId = user.storageRootId
            }
            user
        }
    }

    suspend fun logout() = withContext(Dispatchers.IO) {
        try {
            val req = authed(Request.Builder().url("${base()}/api/auth/device-logout"))
                .post("{}".toRequestBody("application/json".toMediaType()))
                .build()
            http.newCall(req).execute().close()
        } catch (_: Exception) {
            /* ignore */
        } finally {
            session.clearAuth()
        }
    }

    suspend fun listFiles(parentId: String?): List<FileEntry> = withContext(Dispatchers.IO) {
        val url = if (parentId.isNullOrBlank()) {
            "${base()}/api/files"
        } else {
            "${base()}/api/files?parentId=${Uri.encode(parentId)}"
        }
        val req = authed(Request.Builder().url(url)).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(ListSerializer(FileEntry.serializer()), text)
                .sortedWith(compareBy({ it.kind != "folder" }, { it.name.lowercase() }))
        }
    }

    suspend fun createFolder(parentId: String?, name: String): FileEntry = withContext(Dispatchers.IO) {
        val payload = json.encodeToString(
            CreateFolderRequest.serializer(),
            CreateFolderRequest(parentId = parentId, name = name),
        )
        val req = authed(Request.Builder().url("${base()}/api/files"))
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(FileEntry.serializer(), text)
        }
    }

    suspend fun rename(fileId: String, name: String) = withContext(Dispatchers.IO) {
        val payload = json.encodeToString(RenameRequest.serializer(), RenameRequest(fileId, name))
        val req = authed(Request.Builder().url("${base()}/api/files/rename"))
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
        }
    }

    suspend fun delete(fileId: String) = withContext(Dispatchers.IO) {
        val payload = json.encodeToString(DeleteRequest.serializer(), DeleteRequest(fileId))
        val req = authed(Request.Builder().url("${base()}/api/files/delete"))
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
        }
    }

    private val folderCache = ConcurrentHashMap<String, String>()

    suspend fun ensureChildFolder(parentId: String?, name: String): String = withContext(Dispatchers.IO) {
        val cacheKey = (parentId ?: "root") + "/" + name
        folderCache[cacheKey]?.let { return@withContext it }
        val children = listFiles(parentId)
        val existing = children.firstOrNull { it.kind == "folder" && it.name.equals(name, ignoreCase = true) }
        if (existing != null) {
            folderCache[cacheKey] = existing.id
            return@withContext existing.id
        }
        val created = createFolder(parentId, name)
        folderCache[cacheKey] = created.id
        created.id
    }

    suspend fun ensurePhotosAlbumFolder(media: LocalMedia): String {
        val root = if (session.photosRootId.isNotBlank()) {
            session.photosRootId
        } else {
            val id = ensureChildFolder(null, "TR Photos")
            session.photosRootId = id
            id
        }
        val yearId = ensureChildFolder(root, media.year.toString())
        return ensureChildFolder(yearId, "%02d".format(media.month))
    }

    suspend fun upload(parentId: String?, uri: Uri): FileEntry = withContext(Dispatchers.IO) {
        val resolver = appContext.contentResolver
        var name = "upload.bin"
        var size = -1L
        resolver.query(uri, null, null, null, null)?.use { cursor ->
            val nameIdx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            val sizeIdx = cursor.getColumnIndex(OpenableColumns.SIZE)
            if (cursor.moveToFirst()) {
                if (nameIdx >= 0) name = cursor.getString(nameIdx) ?: name
                if (sizeIdx >= 0) size = cursor.getLong(sizeIdx)
            }
        }
        val mime = resolver.getType(uri) ?: "application/octet-stream"
        uploadStream(parentId, name, mime, size) {
            resolver.openInputStream(uri) ?: throw IOException("Dosya okunamadı")
        }
    }

    suspend fun uploadMedia(parentId: String?, media: LocalMedia): FileEntry = withContext(Dispatchers.IO) {
        val resolver = appContext.contentResolver
        uploadStream(parentId, media.displayName, media.mimeType, media.sizeBytes) {
            resolver.openInputStream(media.uri) ?: throw IOException("Medya okunamadı")
        }
    }

    private fun uploadStream(
        parentId: String?,
        name: String,
        mime: String,
        size: Long,
        open: () -> InputStream,
    ): FileEntry {
        val mediaType = mime.toMediaType()
        val fileBody = object : RequestBody() {
            override fun contentType(): MediaType = mediaType
            override fun contentLength(): Long = if (size >= 0) size else -1L
            override fun writeTo(sink: BufferedSink) {
                open().use { input -> sink.writeAll(input.source()) }
            }
        }
        val multipart = MultipartBody.Builder().setType(MultipartBody.FORM)
            .addFormDataPart("file", name, fileBody)
            .apply {
                if (!parentId.isNullOrBlank()) {
                    addFormDataPart("parentId", parentId)
                }
            }
            .build()
        val req = authed(Request.Builder().url("${base()}/api/files/upload"))
            .post(multipart)
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            return json.decodeFromString(FileEntry.serializer(), text)
        }
    }

    suspend fun downloadToCache(entry: FileEntry): File = withContext(Dispatchers.IO) {
        val dir = File(appContext.cacheDir, "downloads").also { it.mkdirs() }
        val dest = File(dir, entry.name)
        val req = authed(Request.Builder().url("${base()}/api/files/download/${entry.id}")).get().build()
        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) {
                throw IOException(parseError(resp.body?.string()))
            }
            resp.body?.byteStream()?.use { input ->
                dest.outputStream().use { output -> input.copyTo(output) }
            } ?: throw IOException("Boş yanıt")
        }
        dest
    }
}
