package net.neciparmagan.trdriver.calls

import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.Serializable

enum class LocalTelProviderType(val wireValue: String) {
    SIP("sip"),
    GATEWAY("gateway");

    companion object {
        fun fromWire(raw: String?): LocalTelProviderType =
            when (raw?.trim()?.lowercase()) {
                "gateway" -> GATEWAY
                else -> SIP
            }
    }
}

enum class LocalTelTransport(val wireValue: String) {
    UDP("udp"),
    TCP("tcp"),
    TLS("tls"),
    WSS("wss"),
    HTTPS("https");

    companion object {
        fun fromWire(raw: String?, providerType: LocalTelProviderType): LocalTelTransport {
            val cleaned = raw?.trim()?.lowercase().orEmpty()
            return entries.firstOrNull { it.wireValue == cleaned }
                ?: if (providerType == LocalTelProviderType.GATEWAY) WSS else UDP
        }
    }
}

data class LocalTelConfig(
    val enabled: Boolean = false,
    val providerType: LocalTelProviderType = LocalTelProviderType.SIP,
    val serverUrl: String = "",
    val sipDomain: String = "",
    val sipPort: Int = 5060,
    val transport: LocalTelTransport = LocalTelTransport.UDP,
    val username: String = "",
    val extension: String = "",
    val displayName: String = "",
    val secret: String = "",
    val outboundPrefix: String = "",
    val defaultCountryCode: String = "90",
    val autoRegister: Boolean = true,
    val syncToServer: Boolean = false,
    val gatewayToken: String = "",
    val gatewayTokenExpiresAt: Long = 0L,
) {
    fun effectiveHost(): String {
        val raw = serverUrl.trim().ifEmpty { sipDomain.trim() }
        if (raw.isEmpty()) return ""
        val withoutScheme = raw
            .removePrefix("sip:")
            .removePrefix("sips:")
            .removePrefix("wss://")
            .removePrefix("ws://")
            .removePrefix("https://")
            .removePrefix("http://")
        return withoutScheme.substringBefore('/').substringBefore(':').trim()
    }

    fun effectiveDomain(): String =
        sipDomain.trim().ifEmpty { effectiveHost() }

    fun effectiveCallerId(): String =
        extension.trim().ifEmpty { username.trim() }

    fun isConfiguredForSip(): Boolean =
        enabled && providerType == LocalTelProviderType.SIP &&
            effectiveHost().isNotEmpty() && username.isNotBlank()

    fun isConfiguredForGateway(trDriverServerUrl: String): Boolean =
        enabled && providerType == LocalTelProviderType.GATEWAY &&
            (serverUrl.isNotBlank() || trDriverServerUrl.isNotBlank())

    fun isReady(trDriverServerUrl: String = ""): Boolean = when (providerType) {
        LocalTelProviderType.SIP -> isConfiguredForSip()
        LocalTelProviderType.GATEWAY -> isConfiguredForGateway(trDriverServerUrl)
    }

    /** Never include secrets, usernames or server credentials in logs. */
    override fun toString(): String =
        "LocalTelConfig(enabled=$enabled, provider=${providerType.wireValue}, transport=${transport.wireValue}, hasSecret=${secret.isNotEmpty()})"
}

enum class ProviderRegistrationState {
    DISABLED,
    REGISTERING,
    REGISTERED,
    ERROR,
}

data class ProviderStatusSnapshot(
    val state: ProviderRegistrationState = ProviderRegistrationState.DISABLED,
    val statusText: String = "Kapalı",
    val lastError: String = "",
)

sealed class CallProviderEvent {
    data class IncomingCall(
        val callId: String,
        val remoteUri: String,
        val displayNameHint: String = "",
    ) : CallProviderEvent()

    data class OutgoingRinging(val callId: String) : CallProviderEvent()
    data class CallConnected(val callId: String) : CallProviderEvent()
    data class CallEnded(val callId: String, val reason: CallEndReason) : CallProviderEvent()
    data class CallError(val callId: String, val reason: CallEndReason, val message: String) : CallProviderEvent()
    /** Server-side missed call: nobody answered within the ring window. */
    data class CallMissed(
        val callId: String,
        val remoteUri: String,
        val displayNameHint: String = "",
    ) : CallProviderEvent()
}

/**
 * Configurable LocalTel IP-phone/SIP provider interface.
 *
 * Implementations encapsulate transport & protocol details (direct RFC 3261
 * SIP+RTP, HTTP/WebSocket gateway, or custom LocalTel vendor endpoints) so the
 * call state machine, Telecom ConnectionService, lock-screen UI, and contact
 * matching remain completely provider-agnostic.
 */
interface LocalTelCallProvider {
    val providerId: String
    val statusFlow: StateFlow<ProviderStatusSnapshot>
    val events: SharedFlow<CallProviderEvent>

    suspend fun start(config: LocalTelConfig): Result<Unit>
    suspend fun stop()
    suspend fun startOutgoingCall(callId: String, targetNumber: String, config: LocalTelConfig): Result<Unit>
    suspend fun acceptIncomingCall(callId: String): Result<Unit>
    suspend fun rejectIncomingCall(callId: String): Result<Unit>
    suspend fun hangupCall(callId: String): Result<Unit>
    suspend fun setMuted(callId: String, muted: Boolean)
    suspend fun sendDtmf(callId: String, digit: Char)
}

@Serializable
data class LocalTelConfigRequestDto(
    val enabled: Boolean,
    val providerType: String,
    val serverUrl: String,
    val sipDomain: String = "",
    val sipPort: Int = 5060,
    val transport: String = "udp",
    val username: String = "",
    val extension: String = "",
    val displayName: String = "",
    val outboundPrefix: String = "",
    val defaultCountryCode: String = "90",
    val autoRegister: Boolean = true,
    val secret: String = "",
    val clearSecret: Boolean = false,
) {
    override fun toString(): String =
        "LocalTelConfigRequestDto(enabled=$enabled, provider=$providerType, hasSecret=${secret.isNotEmpty()})"
}

@Serializable
data class LocalTelConfigViewDto(
    val configured: Boolean = false,
    val enabled: Boolean = false,
    val providerType: String = "sip",
    val serverUrl: String = "",
    val sipDomain: String = "",
    val sipPort: Int = 5060,
    val transport: String = "udp",
    val username: String = "",
    val extension: String = "",
    val displayName: String = "",
    val outboundPrefix: String = "",
    val defaultCountryCode: String = "90",
    val autoRegister: Boolean = true,
    val hasSecret: Boolean = false,
    val updatedAt: String? = null,
    val mode: String = "",
    val autoProvisioned: Boolean = false,
    val gateway: LocalTelGatewayDto? = null,
)

@Serializable
data class LocalTelGatewayDto(
    val wssUrl: String = "",
    val host: String = "",
    val domain: String = "",
    val username: String = "",
    val extension: String = "",
    val secret: String = "",
    val token: String = "",
    val tokenExpiresAt: String? = null,
    val stunUrls: List<String> = emptyList(),
) {
    override fun toString(): String =
        "LocalTelGatewayDto(host=$host, extension=$extension, hasSecret=${secret.isNotEmpty()}, hasToken=${token.isNotEmpty()})"
}

@Serializable
data class LocalTelProvisionDto(
    val configured: Boolean = false,
    val enabled: Boolean = false,
    val providerType: String = "sip",
    val serverUrl: String = "",
    val sipDomain: String = "",
    val sipPort: Int = 5060,
    val transport: String = "udp",
    val username: String = "",
    val extension: String = "",
    val displayName: String = "",
    val outboundPrefix: String = "",
    val defaultCountryCode: String = "90",
    val autoRegister: Boolean = true,
    val hasSecret: Boolean = false,
    val updatedAt: String? = null,
    val secret: String = "",
) {
    override fun toString(): String =
        "LocalTelProvisionDto(configured=$configured, enabled=$enabled, hasSecret=${secret.isNotEmpty()})"
}

@Serializable
data class LocalTelSignalDto(
    val type: String,
    val callId: String = "",
    val number: String = "",
    val displayName: String = "",
    val payload: String = "",
    val deviceId: String = "",
    val timestampMs: Long = 0L,
) {
    override fun toString(): String =
        "LocalTelSignalDto(type=$type, callId=$callId)"
}
