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
