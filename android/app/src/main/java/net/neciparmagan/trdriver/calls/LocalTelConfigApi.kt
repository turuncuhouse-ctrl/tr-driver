package net.neciparmagan.trdriver.calls

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import net.neciparmagan.trdriver.data.ApiError
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.concurrent.TimeUnit

/**
 * Authenticated client for `/api/localtel` endpoints.
 * Never logs payloads, SIP credentials, or numbers.
 */
class LocalTelConfigApi(private val session: SessionStore) {
    private val json = Json {
        encodeDefaults = true
        ignoreUnknownKeys = true
        isLenient = true
    }

    private val http = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .writeTimeout(30, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    fun hasSession(): Boolean = session.serverUrl.isNotBlank() && !session.token.isNullOrBlank()

    private fun baseUrl(): String = session.serverUrl.trimEnd('/')

    private fun authed(url: String): Request.Builder {
        val builder = Request.Builder().url(url)
        val token = session.token
        if (!token.isNullOrBlank()) {
            builder.header("Authorization", "Bearer $token")
        }
        return builder
    }

    private fun parseError(body: String?): String {
        if (body.isNullOrBlank()) return "Sunucu isteği başarısız"
        return runCatching {
            json.decodeFromString(ApiError.serializer(), body).error.ifBlank { "Sunucu isteği başarısız" }
        }.getOrDefault("Sunucu isteği başarısız")
    }

    suspend fun getConfig(): Result<LocalTelConfigViewDto> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) error("TR Driver oturumu açık değil")
            val req = authed("${baseUrl()}/api/localtel/config").get().build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseError(text))
                json.decodeFromString(LocalTelConfigViewDto.serializer(), text)
            }
        }
    }

    /** Fetches the zero-configuration account and its short-lived WS token. */
    suspend fun fetchGatewayConfig(): Result<LocalTelConfig> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) error("TR Driver oturumu açık değil")
            val req = authed("${baseUrl()}/api/localtel/config").get().build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseError(text))
                val dto = json.decodeFromString(LocalTelConfigViewDto.serializer(), text)
                val gateway = dto.gateway
                if (gateway == null) {
                    error("Sunucu otomatik arama hesabı sağlamadı")
                }
                LocalTelConfig(
                    enabled = dto.enabled,
                    providerType = LocalTelProviderType.GATEWAY,
                    serverUrl = gateway.wssUrl,
                    sipDomain = gateway.domain,
                    sipPort = 443,
                    transport = LocalTelTransport.WSS,
                    username = gateway.username,
                    extension = gateway.extension,
                    displayName = dto.displayName,
                    secret = gateway.secret,
                    defaultCountryCode = dto.defaultCountryCode.ifBlank { "90" },
                    autoRegister = true,
                    syncToServer = true,
                    gatewayToken = gateway.token,
                    gatewayTokenExpiresAt = gateway.tokenExpiresAt?.let {
                        runCatching { java.time.Instant.parse(it).toEpochMilli() }.getOrDefault(0L)
                    } ?: 0L,
                )
            }
        }
    }

    /** Own phone number + extension ("Kendi numaram"). */
    suspend fun getMyPhone(): Result<Pair<String, String>> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) error("TR Driver oturumu açık değil")
            val req = authed("${baseUrl()}/api/localtel/phone").get().build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseError(text))
                parsePhone(text)
            }
        }
    }

    suspend fun setMyPhone(phone: String): Result<Pair<String, String>> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) error("TR Driver oturumu açık değil")
            val body = json.encodeToString(
                kotlinx.serialization.json.JsonObject.serializer(),
                kotlinx.serialization.json.buildJsonObject {
                    put("phone", kotlinx.serialization.json.JsonPrimitive(phone.trim()))
                },
            ).toRequestBody("application/json; charset=utf-8".toMediaType())
            val req = authed("${baseUrl()}/api/localtel/phone").put(body).build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseError(text))
                parsePhone(text)
            }
        }
    }

    private fun parsePhone(text: String): Pair<String, String> {
        val obj = json.parseToJsonElement(text) as kotlinx.serialization.json.JsonObject
        val phone = (obj["phone"] as? kotlinx.serialization.json.JsonPrimitive)?.content.orEmpty()
        val ext = (obj["extension"] as? kotlinx.serialization.json.JsonPrimitive)?.content.orEmpty()
        return phone to ext
    }

    data class DirectoryMember(val name: String, val extension: String, val online: Boolean)

    suspend fun getDirectory(): Result<List<DirectoryMember>> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) error("TR Driver oturumu açık değil")
            val req = authed("${baseUrl()}/api/localtel/directory").get().build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseError(text))
                val arr = json.parseToJsonElement(text) as kotlinx.serialization.json.JsonArray
                arr.mapNotNull { el ->
                    val o = el as? kotlinx.serialization.json.JsonObject ?: return@mapNotNull null
                    fun str(k: String) = (o[k] as? kotlinx.serialization.json.JsonPrimitive)?.content.orEmpty()
                    val ext = str("extension")
                    if (ext.isBlank()) null else DirectoryMember(
                        name = str("name").ifBlank { ext },
                        extension = ext,
                        online = str("online") == "true",
                    )
                }
            }
        }
    }

    suspend fun provisionConfig(): Result<LocalTelConfig> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) error("TR Driver oturumu açık değil")
            val req = authed("${baseUrl()}/api/localtel/config/provision").get().build()
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                if (!resp.isSuccessful) error(parseError(text))
                val dto = json.decodeFromString(LocalTelProvisionDto.serializer(), text)
                val provider = LocalTelProviderType.fromWire(dto.providerType)
                LocalTelConfig(
                    enabled = dto.enabled,
                    providerType = provider,
                    serverUrl = dto.serverUrl,
                    sipDomain = dto.sipDomain,
                    sipPort = dto.sipPort,
                    transport = LocalTelTransport.fromWire(dto.transport, provider),
                    username = dto.username,
                    extension = dto.extension,
                    displayName = dto.displayName,
                    secret = dto.secret,
                    outboundPrefix = dto.outboundPrefix,
                    defaultCountryCode = dto.defaultCountryCode,
                    autoRegister = dto.autoRegister,
                    syncToServer = true,
                )
            }
        }
    }

    suspend fun saveConfig(config: LocalTelConfig, clearSecret: Boolean = false): Result<LocalTelConfigViewDto> =
        withContext(Dispatchers.IO) {
            runCatching {
                if (!hasSession()) error("TR Driver oturumu açık değil")
                val payload = LocalTelConfigRequestDto(
                    enabled = config.enabled,
                    providerType = config.providerType.wireValue,
                    serverUrl = config.serverUrl.trim(),
                    sipDomain = config.sipDomain.trim(),
                    sipPort = config.sipPort,
                    transport = config.transport.wireValue,
                    username = config.username.trim(),
                    extension = config.extension.trim(),
                    displayName = config.displayName.trim(),
                    outboundPrefix = config.outboundPrefix.trim(),
                    defaultCountryCode = config.defaultCountryCode.ifBlank { "90" },
                    autoRegister = config.autoRegister,
                    secret = if (clearSecret) "" else config.secret,
                    clearSecret = clearSecret,
                )
                val body = json.encodeToString(LocalTelConfigRequestDto.serializer(), payload)
                    .toRequestBody("application/json; charset=utf-8".toMediaType())
                val req = authed("${baseUrl()}/api/localtel/config").put(body).build()
                http.newCall(req).execute().use { resp ->
                    val text = resp.body?.string().orEmpty()
                    if (!resp.isSuccessful) error(parseError(text))
                    json.decodeFromString(LocalTelConfigViewDto.serializer(), text)
                }
            }
        }

    suspend fun deleteConfig(): Result<Unit> = withContext(Dispatchers.IO) {
        runCatching {
            if (!hasSession()) return@runCatching Unit
            val req = authed("${baseUrl()}/api/localtel/config").delete().build()
            http.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful && resp.code != 404) {
                    error(parseError(resp.body?.string()))
                }
            }
        }
    }
}
