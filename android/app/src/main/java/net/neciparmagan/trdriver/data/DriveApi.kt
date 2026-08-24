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
        .retryOnConnectionFailure(true)
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
            val deviceName = session.deviceName.ifBlank { "Android-" + (Build.MODEL ?: "Phone") }
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
                .let { FileLists.sortDrive(it) }
        }
    }

    suspend fun search(query: String): List<FileEntry> = withContext(Dispatchers.IO) {
        val url = "${base()}/api/files/search?q=${Uri.encode(query.trim())}"
        val req = authed(Request.Builder().url(url)).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(ListSerializer(FileEntry.serializer()), text)
                .let { FileLists.sortDrive(it) }
        }
    }

    suspend fun createShareLink(entryId: String): ShareCreateResponse = withContext(Dispatchers.IO) {
        val payload = json.encodeToString(
            ShareCreateRequest.serializer(),
            ShareCreateRequest(entryId = entryId),
        )
        val req = authed(Request.Builder().url("${base()}/api/shares"))
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(ShareCreateResponse.serializer(), text)
        }
    }

    suspend fun setStarred(entryId: String, starred: Boolean) = withContext(Dispatchers.IO) {
        val payload = json.encodeToString(StarRequest.serializer(), StarRequest(entryId, starred))
        val req = authed(Request.Builder().url("${base()}/api/files/starred"))
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
        }
    }

    suspend fun listStarred(): List<FileEntry> = withContext(Dispatchers.IO) {
        val req = authed(Request.Builder().url("${base()}/api/files/starred")).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(ListSerializer(FileEntry.serializer()), text)
                .let { FileLists.sortDrive(it) }
        }
    }

    suspend fun listRecent(): List<FileEntry> = withContext(Dispatchers.IO) {
        val req = authed(Request.Builder().url("${base()}/api/files/recent")).get().build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(ListSerializer(FileEntry.serializer()), text)
                .let { FileLists.sortDrive(it) }
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
    private val photosMonthCache = ConcurrentHashMap<String, String>()

    @Volatile
    private var cachedPace: UploadPace? = null

    @Volatile
    private var lastPaceFetchMs: Long = 0L

    private companion object {
        const val PACE_TTL_MS = 45_000L
        /** Client-side ~25% speed boost on server-advised delays (backup/manual upload). */
        const val PACE_DELAY_FACTOR = 0.75
    }

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
        val monthKey = "${media.year}/${"%02d".format(media.month)}"
        photosMonthCache[monthKey]?.let { return it }
        val device = sanitizeFolder(session.deviceName.ifBlank { android.os.Build.MODEL ?: "Android" })
        val root = if (session.photosRootId.isNotBlank()) {
            session.photosRootId
        } else {
            val id = ensureChildFolder(null, "TR Photos")
            session.photosRootId = id
            id
        }
        val deviceId = ensureChildFolder(root, device)
        val yearId = ensureChildFolder(deviceId, media.year.toString())
        val monthId = ensureChildFolder(yearId, "%02d".format(media.month))
        photosMonthCache[monthKey] = monthId
        return monthId
    }

    /** TR Backup / {device} / {folderName} */
    suspend fun ensureBackupFolder(folderName: String): String {
        val device = sanitizeFolder(session.deviceName.ifBlank { android.os.Build.MODEL ?: "Android" })
        val root = ensureChildFolder(null, "TR Backup")
        val deviceId = ensureChildFolder(root, device)
        return ensureChildFolder(deviceId, sanitizeFolder(folderName))
    }

    /** TR Araç Kabul / {PLAKA} — oto servis araç teslim klasörü */
    suspend fun ensureVehicleIntakeFolder(plate: String): FileEntry {
        val name = sanitizePlate(plate)
        if (name.isBlank()) throw IOException("Plaka gerekli")
        val root = ensureChildFolder(null, "TR Araç Kabul")
        val id = ensureChildFolder(root, name)
        return FileEntry(id = id, name = name, kind = "folder", parentId = root)
    }

    private fun sanitizePlate(plate: String): String {
        val cleaned = plate
            .uppercase(java.util.Locale("tr", "TR"))
            .replace(Regex("[\\\\/:*?\"<>|]"), " ")
            .replace(Regex("\\s+"), " ")
            .trim()
            .take(24)
        return cleaned
    }

    private fun sanitizeFolder(name: String): String {
        val cleaned = name.replace(Regex("[\\\\/:*?\"<>|]"), "_").trim().take(60)
        return cleaned.ifBlank { "Android" }
    }

    suspend fun register(email: String, password: String, displayName: String): DeviceLoginResponse = withContext(Dispatchers.IO) {
        val payload = json.encodeToString(
            RegisterRequest.serializer(),
            RegisterRequest(email = email, password = password, displayName = displayName),
        )
        val req = Request.Builder()
            .url("${base()}/api/auth/register")
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
        }
        login(email, password)
    }

    suspend fun redeemQr(challengeToken: String): DeviceLoginResponse = withContext(Dispatchers.IO) {
        val deviceName = session.deviceName.ifBlank { "Android-" + (Build.MODEL ?: "Phone") }
        val body = json.encodeToString(
            QrRedeemRequest.serializer(),
            QrRedeemRequest(challengeToken = challengeToken, deviceName = deviceName),
        ).toRequestBody("application/json".toMediaType())
        val req = Request.Builder()
            .url("${base()}/api/auth/qr/redeem")
            .post(body)
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            val parsed = json.decodeFromString(DeviceLoginResponse.serializer(), text)
            if (!parsed.token.isNullOrBlank()) {
                session.token = parsed.token
                session.email = parsed.user?.email.orEmpty()
                session.storageRootId = parsed.user?.storageRootId.orEmpty()
            }
            parsed
        }
    }

    fun downloadUrl(fileId: String): String = "${base()}/api/files/download/$fileId"

    suspend fun upload(
        parentId: String?,
        uri: Uri,
        onProgress: ((bytesSent: Long, totalBytes: Long) -> Unit)? = null,
        onRetry: ((attempt: Int, error: Throwable) -> Unit)? = null,
    ): FileEntry = UploadRetry.run(appContext, onRetry = onRetry) {
        refreshUploadPaceIfStale()
        UploadThrottle.run {
            withContext(Dispatchers.IO) {
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
                uploadStream(parentId, name, mime, size, onProgress) {
                    resolver.openInputStream(uri) ?: throw IOException("Dosya okunamadı")
                }
            }
        }
    }

    suspend fun uploadMedia(
        parentId: String?,
        media: LocalMedia,
        onProgress: ((bytesSent: Long, totalBytes: Long) -> Unit)? = null,
        onRetry: ((attempt: Int, error: Throwable) -> Unit)? = null,
    ): FileEntry = UploadRetry.run(appContext, onRetry = onRetry) {
        refreshUploadPaceIfStale()
        UploadThrottle.run {
            withContext(Dispatchers.IO) {
                val resolver = appContext.contentResolver
                uploadStream(parentId, media.displayName, media.mimeType, media.sizeBytes, onProgress) {
                    resolver.openInputStream(media.uri) ?: throw IOException("Medya okunamadı")
                }
            }
        }
    }

    /** Force refresh at backup batch start; uploads use TTL cache to avoid per-file round-trips. */
    suspend fun refreshUploadPace(force: Boolean = false): UploadPace = withContext(Dispatchers.IO) {
        val now = System.currentTimeMillis()
        if (!force) {
            cachedPace?.let { pace ->
                if (now - lastPaceFetchMs < PACE_TTL_MS) return@withContext pace
            }
        }
        try {
            val req = authed(Request.Builder().url("${base()}/api/files/upload-pace")).get().build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) return@withContext cachedPace ?: UploadPace()
                val pace = json.decodeFromString(UploadPace.serializer(), text)
                applyPaceToThrottle(pace)
                cachedPace = pace
                lastPaceFetchMs = now
                pace
            }
        } catch (_: Exception) {
            cachedPace ?: UploadPace()
        }
    }

    private suspend fun refreshUploadPaceIfStale() {
        refreshUploadPace(force = false)
    }

    private fun applyPaceToThrottle(pace: UploadPace) {
        val boostedBatch = (pace.recommendedBatch * 1.25).toInt().coerceIn(1, 25)
        val boostedDelay = (pace.delayMs * PACE_DELAY_FACTOR).toInt().coerceIn(40, 8_000)
        UploadThrottle.applyPace(
            pace.copy(
                recommendedBatch = boostedBatch,
                delayMs = boostedDelay,
            ),
        )
    }

    private fun uploadStream(
        parentId: String?,
        name: String,
        mime: String,
        size: Long,
        onProgress: ((bytesSent: Long, totalBytes: Long) -> Unit)?,
        open: () -> InputStream,
    ): FileEntry {
        val mediaType = mime.toMediaType()
        val fileBody = object : RequestBody() {
            override fun contentType(): MediaType = mediaType
            override fun contentLength(): Long = if (size >= 0) size else -1L
            override fun isOneShot(): Boolean = true
            override fun writeTo(sink: BufferedSink) {
                open().use { input ->
                    val buffer = ByteArray(128 * 1024)
                    var sent = 0L
                    var lastEmit = -1L
                    while (true) {
                        val read = input.read(buffer)
                        if (read < 0) break
                        sink.write(buffer, 0, read)
                        sent += read
                        val total = if (size >= 0) size else sent
                        if (onProgress != null) {
                            val step = maxOf(256L * 1024L, total / 50L)
                            if (lastEmit < 0L || sent - lastEmit >= step || sent >= total) {
                                lastEmit = sent
                                onProgress(sent, total)
                            }
                        }
                    }
                    if (onProgress != null) {
                        onProgress(sent, if (size >= 0) size else sent)
                    }
                }
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
            if (!resp.isSuccessful) {
                val retryAfter = resp.header("Retry-After")?.toIntOrNull() ?: 0
                if (resp.code == 429) {
                    throw HttpStatusIOException(
                        code = 429,
                        message = parseError(text).ifBlank { "Sunucu yoğun" },
                        retryAfterSec = retryAfter.coerceAtLeast(2),
                    )
                }
                throw IOException(parseError(text))
            }
            return json.decodeFromString(FileEntry.serializer(), text)
        }
    }

    suspend fun downloadToCache(entry: FileEntry): File = withContext(Dispatchers.IO) {
        val dir = File(appContext.cacheDir, "downloads").also { it.mkdirs() }
        val safeName = entry.name
            .replace('/', '_')
            .replace('\\', '_')
            .replace('\u0000', '_')
            .ifBlank { "download.bin" }
        val dest = File(dir, safeName)
        val req = authed(Request.Builder().url("${base()}/api/files/download/${entry.id}")).get().build()
        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) {
                throw IOException(parseError(resp.body?.string()))
            }
            resp.body?.byteStream()?.use { input ->
                dest.outputStream().use { output -> input.copyTo(output) }
            } ?: throw IOException("Boş yanıt")
        }
        if (!dest.exists() || dest.length() == 0L) {
            throw IOException("İndirilen dosya boş")
        }
        dest
    }

    suspend fun fetchAndroidVersion(): AndroidVersionInfo = withContext(Dispatchers.IO) {
        val req = Request.Builder()
            .url("${base()}/api/android/version")
            .get()
            .header("Cache-Control", "no-cache")
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) throw IOException(parseError(text))
            json.decodeFromString(AndroidVersionInfo.serializer(), text)
        }
    }

    suspend fun downloadApkUpdate(downloadURL: String): File = withContext(Dispatchers.IO) {
        val path = when {
            downloadURL.startsWith("http://") || downloadURL.startsWith("https://") -> downloadURL
            downloadURL.startsWith("/") -> "${base()}$downloadURL"
            else -> "${base()}/$downloadURL"
        }
        val dir = File(appContext.cacheDir, "updates").also { it.mkdirs() }
        val dest = File(dir, "TRDriver-update.apk")
        if (dest.exists()) dest.delete()
        val req = Request.Builder().url(path).get().build()
        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) {
                throw IOException(parseError(resp.body?.string()))
            }
            resp.body?.byteStream()?.use { input ->
                dest.outputStream().use { output -> input.copyTo(output) }
            } ?: throw IOException("Boş APK yanıtı")
        }
        if (!dest.exists() || dest.length() < 512 * 1024) {
            throw IOException("APK indirilemedi veya çok küçük")
        }
        dest
    }
}
