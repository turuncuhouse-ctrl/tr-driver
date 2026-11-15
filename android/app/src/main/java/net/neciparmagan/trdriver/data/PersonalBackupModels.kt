package net.neciparmagan.trdriver.data

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement

// ---------------------------------------------------------------------------
// Wire contract with the server (internal/personalbackup).
// Field names must match the Go structs exactly: the server rejects unknown
// fields, so any extra key here would make the whole batch fail.
// ---------------------------------------------------------------------------

@Serializable
data class PersonalBackupItem(
    val key: String,
    val revision: Long,
    val hash: String,
    val data: JsonElement,
)

@Serializable
data class PersonalBackupSyncRequest(
    val deviceId: String = "",
    val items: List<PersonalBackupItem> = emptyList(),
    val deleted: List<String> = emptyList(),
)

@Serializable
data class PersonalBackupSyncResult(
    val inserted: Int = 0,
    val updated: Int = 0,
    val unchanged: Int = 0,
    val stale: Int = 0,
    val deleted: Int = 0,
    val total: Int = 0,
    val syncedAt: String = "",
) {
    val accepted: Int get() = inserted + updated
}

@Serializable
data class PersonalBackupListItem(
    val key: String = "",
    val revision: Long = 0,
    val hash: String = "",
    val data: JsonElement? = null,
    val updatedAt: String = "",
)

@Serializable
data class PersonalBackupListPage(
    val kind: String = "",
    val items: List<PersonalBackupListItem> = emptyList(),
    val nextAfter: String? = null,
    val total: Int = 0,
)

@Serializable
data class PersonalBackupKindStatus(
    val kind: String = "",
    val count: Int = 0,
    val lastSyncAt: String? = null,
)

@Serializable
data class PersonalBackupStatus(
    val kinds: List<PersonalBackupKindStatus> = emptyList(),
)

// ---------------------------------------------------------------------------
// Local records. Kept flat and nullable-free where possible so the encoded
// JSON (and therefore its SHA-256) is stable across devices and app versions.
// ---------------------------------------------------------------------------

@Serializable
data class ContactPhone(
    val type: String = "mobile",
    val label: String = "",
    val value: String = "",
)

@Serializable
data class ContactEmail(
    val type: String = "",
    val value: String = "",
)

@Serializable
data class ContactBackupRecord(
    val displayName: String = "",
    val nickname: String = "",
    val phones: List<ContactPhone> = emptyList(),
    val emails: List<ContactEmail> = emptyList(),
    val addresses: List<String> = emptyList(),
    val organization: String = "",
    val jobTitle: String = "",
    val note: String = "",
    val birthday: String = "",
    val websites: List<String> = emptyList(),
    val starred: Boolean = false,
    @SerialName("lastUpdatedMs") val lastUpdatedMs: Long = 0,
)

@Serializable
data class SmsBackupRecord(
    val address: String = "",
    val body: String = "",
    val dateMs: Long = 0,
    /** inbox | sent | draft | outbox | failed | queued */
    val type: String = "inbox",
    val read: Boolean = true,
    val threadId: Long = 0,
    val serviceCenter: String = "",
    val subject: String = "",
)
