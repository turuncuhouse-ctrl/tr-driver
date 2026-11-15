package net.neciparmagan.trdriver.data

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement

/**
 * JSON codec for personal backup records.
 *
 * The encoded string is both what is uploaded and what is hashed, so the change
 * detection hash can never disagree with the stored payload. Defaults are
 * omitted (`encodeDefaults = false`) to keep records small and stable.
 */
object PersonalBackupCodec {
    val json: Json = Json {
        encodeDefaults = false
        ignoreUnknownKeys = true
        isLenient = true
    }

    fun encodeContact(record: ContactBackupRecord): String =
        json.encodeToString(ContactBackupRecord.serializer(), record)

    fun encodeSms(record: SmsBackupRecord): String =
        json.encodeToString(SmsBackupRecord.serializer(), record)

    fun toElement(payload: String): JsonElement = json.parseToJsonElement(payload)
}
