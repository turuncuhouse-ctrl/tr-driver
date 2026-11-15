package net.neciparmagan.trdriver.data

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Contract tests for the JSON that goes over the wire.
 *
 * The server (`internal/personalbackup`) rejects unknown fields, so these
 * assertions protect the batch from a silent 400 in production.
 */
class PersonalBackupCodecTest {

    @Test
    fun contactRecordKeepsPersonalFields() {
        val json = PersonalBackupCodec.encodeContact(
            ContactBackupRecord(
                displayName = "Ayşe Yılmaz",
                phones = listOf(ContactPhone(type = "2", label = "", value = "+905551112233")),
                emails = listOf(ContactEmail(type = "1", value = "ayse@example.com")),
                note = "Servis kaydı",
                lastUpdatedMs = 1_700_000_000_000L,
            ),
        )
        val obj = Json.parseToJsonElement(json).jsonObject
        assertEquals("Ayşe Yılmaz", obj["displayName"]!!.jsonPrimitive.content)
        assertEquals(1_700_000_000_000L, obj["lastUpdatedMs"]!!.jsonPrimitive.content.toLong())
        assertTrue(json.contains("ayse@example.com"))
        assertTrue(json.contains("+905551112233"))
    }

    @Test
    fun encodingIsDeterministicSoHashesAreStable() {
        val record = ContactBackupRecord(
            displayName = "Mehmet",
            phones = listOf(ContactPhone(value = "+905554445566")),
            lastUpdatedMs = 42L,
        )
        val first = PersonalBackupCodec.encodeContact(record)
        val second = PersonalBackupCodec.encodeContact(record)
        assertEquals(first, second)
        assertEquals(
            PersonalBackupPolicy.contentHash(first),
            PersonalBackupPolicy.contentHash(second),
        )
    }

    @Test
    fun smsRecordEncodesTypeAndReadFlag() {
        val json = PersonalBackupCodec.encodeSms(
            SmsBackupRecord(
                address = "+905551112233",
                body = "Yarın 09:00",
                dateMs = 1_700_000_000_000L,
                type = "sent",
                read = false,
                threadId = 7,
            ),
        )
        val obj = Json.parseToJsonElement(json).jsonObject
        assertEquals("sent", obj["type"]!!.jsonPrimitive.content)
        assertEquals("false", obj["read"]!!.jsonPrimitive.content)
        assertFalse(json.contains("displayName"))
    }

    @Test
    fun syncRequestUsesServerFieldNames() {
        val request = PersonalBackupSyncRequest(
            deviceId = "Pixel 7",
            items = listOf(
                PersonalBackupItem(
                    key = PersonalBackupPolicy.contactKey("A", listOf("+905551112233"), emptyList()),
                    revision = 5L,
                    hash = "aa".repeat(32),
                    data = PersonalBackupCodec.toElement("""{"displayName":"A"}"""),
                ),
            ),
            deleted = listOf("bb".repeat(21)),
        )
        val json = PersonalBackupCodec.json.encodeToString(
            PersonalBackupSyncRequest.serializer(),
            request,
        )
        val obj = Json.parseToJsonElement(json).jsonObject
        assertTrue(obj.containsKey("deviceId"))
        assertTrue(obj.containsKey("items"))
        assertTrue(obj.containsKey("deleted"))
        // The server rejects unknown fields.
        assertEquals(setOf("deviceId", "items", "deleted"), obj.keys)
        val item = obj["items"]!!.let { Json.parseToJsonElement(it.toString()).jsonObject }
        assertEquals(setOf("key", "revision", "hash", "data"), item.keys)
    }

    @Test
    fun syncResultDecodesServerResponse() {
        val raw = """
            {"inserted":2,"updated":1,"unchanged":3,"stale":0,"deleted":1,"total":5,
             "syncedAt":"2026-11-15T10:00:00Z"}
        """.trimIndent()
        val result = PersonalBackupCodec.json.decodeFromString(
            PersonalBackupSyncResult.serializer(),
            raw,
        )
        assertEquals(2, result.inserted)
        assertEquals(1, result.updated)
        assertEquals(3, result.unchanged)
        assertEquals(1, result.deleted)
        assertEquals(5, result.total)
        assertEquals(3, result.accepted)
    }

    @Test
    fun listPageDecodesRestorePayload() {
        val raw = """
            {"kind":"contacts","nextAfter":"abc","total":1,"items":[
              {"key":"k","revision":7,"hash":"hh","updatedAt":"2026-11-15T10:00:00Z",
               "data":{"displayName":"Ayşe","phones":[{"type":"2","label":"","value":"+905551112233"}]}}
            ]}
        """.trimIndent()
        val page = PersonalBackupCodec.json.decodeFromString(
            PersonalBackupListPage.serializer(),
            raw,
        )
        assertEquals("contacts", page.kind)
        assertEquals(1, page.items.size)
        assertEquals(7L, page.items[0].revision)
        assertEquals("abc", page.nextAfter)
        assertEquals("Ayşe", page.items[0].data!!.jsonObject["displayName"]!!.jsonPrimitive.content)
    }

    @Test
    fun statusDecodesPerKindCounts() {
        val raw = """{"kinds":[{"kind":"contacts","count":12},{"kind":"sms","count":3}]}"""
        val status = PersonalBackupCodec.json.decodeFromString(
            PersonalBackupStatus.serializer(),
            raw,
        )
        assertEquals(2, status.kinds.size)
        assertEquals(12, status.kinds[0].count)
        assertEquals(3, status.kinds[1].count)
    }
}
