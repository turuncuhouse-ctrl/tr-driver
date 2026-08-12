package net.neciparmagan.trdriver.data

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class User(
    val id: String = "",
    val email: String = "",
    @SerialName("displayName") val displayName: String = "",
    @SerialName("storageRootId") val storageRootId: String = "",
    @SerialName("quotaBytes") val quotaBytes: Long = 0,
    @SerialName("usedBytes") val usedBytes: Long = 0,
)

@Serializable
data class DeviceLoginResponse(
    val token: String? = null,
    val user: User? = null,
    @SerialName("requires2FA") val requires2FA: Boolean = false,
    @SerialName("challengeToken") val challengeToken: String? = null,
    val message: String? = null,
    val error: String? = null,
)

@Serializable
data class FileEntry(
    val id: String,
    val name: String,
    val kind: String,
    @SerialName("parentId") val parentId: String? = null,
    @SerialName("sizeBytes") val sizeBytes: Long = 0,
    @SerialName("mimeType") val mimeType: String = "",
    val starred: Boolean = false,
)

@Serializable
data class ApiError(val error: String = "")

@Serializable
data class CreateFolderRequest(
    @SerialName("parentId") val parentId: String? = null,
    val name: String,
)

@Serializable
data class DeviceLoginRequest(
    val email: String,
    val password: String,
    val deviceName: String,
    val challengeToken: String? = null,
    val code: String? = null,
)

@Serializable
data class RenameRequest(
    @SerialName("fileId") val fileId: String,
    val name: String,
)

@Serializable
data class RegisterRequest(
    val email: String,
    val password: String,
    val displayName: String,
)

@Serializable
data class QrRedeemRequest(
    val challengeToken: String,
    val deviceName: String,
)

@Serializable
data class DeleteRequest(
    @SerialName("fileId") val fileId: String,
)
