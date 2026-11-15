package net.neciparmagan.trdriver.data

import android.content.Context
import android.database.Cursor
import android.provider.ContactsContract
import android.provider.Telephony
import android.util.Log

/**
 * One record read from the phone, ready to be diffed and pushed.
 * The payload string is exactly what is hashed and uploaded.
 */
data class ScannedRecord(
    val key: String,
    val revision: Long,
    val hash: String,
    val dataJson: String,
)

/**
 * Result of a provider scan.
 *
 * [complete] is false when the provider raised (permission revoked mid-scan,
 * OEM database error). An incomplete scan must never be used to infer
 * deletions — see [PersonalBackupPolicy.pendingDeletions].
 */
data class ScanResult(
    val records: List<ScannedRecord> = emptyList(),
    val complete: Boolean = true,
    val skippedOversized: Int = 0,
    /** Records whose content hash is already on the server (payload dropped). */
    val unchanged: Int = 0,
    /**
     * Every key the provider returned, including unchanged and oversized rows.
     * Deletion detection compares against this set, never against the payload
     * list, so a skipped (not changed) record is never mistaken for a deleted
     * one.
     */
    val keys: Set<String> = emptySet(),
)

/** Reads the device address book into stable, content-hashed records. */
object ContactsBackupSource {
    private const val TAG = "ContactsBackup"
    private const val MAX_LIST_ITEMS = 20
    private const val MAX_NOTE_CHARS = 4_000
    private const val MAX_FIELD_CHARS = 200

    private data class ContactAccumulator(
        val displayName: String,
        val updatedMs: Long,
        val starred: Boolean,
    ) {
        val phones = ArrayList<ContactPhone>()
        val emails = ArrayList<ContactEmail>()
        val addresses = ArrayList<String>()
        val websites = ArrayList<String>()
        var organization = ""
        var jobTitle = ""
        var note = ""
        var nickname = ""
        var birthday = ""
    }

    /**
     * @param skipIfUnchanged called with the derived key and content hash; when
     *   it returns true the payload is dropped immediately, so a large address
     *   book never has to be held in memory just to be re-hashed.
     */
    fun read(
        context: Context,
        skipIfUnchanged: (String, String) -> Boolean = { _, _ -> false },
    ): ScanResult {
        var complete = true
        var unchanged = 0
        val contacts = HashMap<Long, ContactAccumulator>()
        try {
            context.contentResolver.query(
                ContactsContract.Contacts.CONTENT_URI,
                CONTACT_PROJECTION,
                null,
                null,
                null,
            )?.use { cursor ->
                while (cursor.moveToNext()) {
                    val id = cursor.getLong(0)
                    val name = cursor.getString(1).orEmpty()
                    val updated = if (cursor.isNull(2)) 0L else cursor.getLong(2)
                    val starred = !cursor.isNull(3) && cursor.getInt(3) != 0
                    contacts[id] = ContactAccumulator(name, updated, starred)
                }
            } ?: run { complete = false }
        } catch (e: Exception) {
            Log.w(TAG, "contacts query failed: ${e.javaClass.simpleName}")
            complete = false
        }

        try {
            context.contentResolver.query(
                ContactsContract.Data.CONTENT_URI,
                DATA_PROJECTION,
                null,
                null,
                null,
            )?.use { cursor -> readDataRows(cursor, contacts) } ?: run { complete = false }
        } catch (e: Exception) {
            Log.w(TAG, "contact data query failed: ${e.javaClass.simpleName}")
            complete = false
        }

        val records = ArrayList<ScannedRecord>(contacts.size)
        val keys = HashSet<String>(contacts.size)
        var oversized = 0
        for (acc in contacts.values) {
            if (acc.displayName.isBlank() && acc.phones.isEmpty() && acc.emails.isEmpty()) continue
            val key = PersonalBackupPolicy.contactKey(
                acc.displayName,
                acc.phones.map { it.value },
                acc.emails.map { it.value },
            )
            keys.add(key)
            val payload = PersonalBackupCodec.encodeContact(
                ContactBackupRecord(
                    displayName = acc.displayName.trim(),
                    nickname = acc.nickname,
                    phones = acc.phones.distinctBy { it.value }.take(MAX_LIST_ITEMS),
                    emails = acc.emails.distinctBy { it.value.lowercase() }.take(MAX_LIST_ITEMS),
                    addresses = acc.addresses.distinct().take(MAX_LIST_ITEMS),
                    organization = acc.organization,
                    jobTitle = acc.jobTitle,
                    note = acc.note,
                    birthday = acc.birthday,
                    websites = acc.websites.distinct().take(MAX_LIST_ITEMS),
                    starred = acc.starred,
                    lastUpdatedMs = acc.updatedMs,
                ),
            )
            if (!PersonalBackupPolicy.allowsItem(
                    PersonalBackupPolicy.KIND_CONTACTS,
                    payload.toByteArray(Charsets.UTF_8).size,
                )
            ) {
                oversized++
                continue
            }
            val hash = PersonalBackupPolicy.contentHash(payload)
            if (skipIfUnchanged(key, hash)) {
                unchanged++
                continue
            }
            records.add(
                ScannedRecord(
                    key = key,
                    revision = acc.updatedMs,
                    hash = hash,
                    dataJson = payload,
                ),
            )
        }
        return ScanResult(
            records = records,
            complete = complete,
            skippedOversized = oversized,
            unchanged = unchanged,
            keys = keys,
        )
    }

    private fun readDataRows(
        cursor: Cursor,
        contacts: HashMap<Long, ContactAccumulator>,
    ) {
        while (cursor.moveToNext()) {
            val contactId = cursor.getLong(0)
            val acc = contacts[contactId] ?: continue
            val mime = cursor.getString(1).orEmpty()
            fun value(index: Int): String = if (cursor.isNull(index)) "" else cursor.getString(index).orEmpty()
            when (mime) {
                ContactsContract.CommonDataKinds.Phone.CONTENT_ITEM_TYPE -> {
                    val number = value(2).trim()
                    if (number.isNotEmpty()) {
                        acc.phones.add(
                            ContactPhone(
                                type = value(3),
                                label = value(4).trim().take(MAX_FIELD_CHARS),
                                value = number.take(MAX_FIELD_CHARS),
                            ),
                        )
                    }
                }
                ContactsContract.CommonDataKinds.Email.CONTENT_ITEM_TYPE -> {
                    val mail = value(2).trim()
                    if (mail.isNotEmpty()) {
                        acc.emails.add(ContactEmail(type = value(3), value = mail.take(MAX_FIELD_CHARS)))
                    }
                }
                ContactsContract.CommonDataKinds.StructuredPostal.CONTENT_ITEM_TYPE -> {
                    // DATA4..DATA10 hold the structured address parts.
                    val parts = listOf(5, 6, 7, 8, 9, 10, 11).map { value(it) }
                        .filter { it.isNotBlank() }
                    if (parts.isNotEmpty()) {
                        acc.addresses.add(parts.joinToString(", ").take(MAX_FIELD_CHARS))
                    }
                }
                ContactsContract.CommonDataKinds.Organization.CONTENT_ITEM_TYPE -> {
                    // Organization: DATA1 = company, DATA4 = title.
                    acc.organization = value(2).trim().take(MAX_FIELD_CHARS)
                    acc.jobTitle = value(5).trim().take(MAX_FIELD_CHARS)
                }
                ContactsContract.CommonDataKinds.Note.CONTENT_ITEM_TYPE -> {
                    acc.note = value(2).trim().take(MAX_NOTE_CHARS)
                }
                ContactsContract.CommonDataKinds.Nickname.CONTENT_ITEM_TYPE -> {
                    acc.nickname = value(2).trim().take(MAX_FIELD_CHARS)
                }
                ContactsContract.CommonDataKinds.Website.CONTENT_ITEM_TYPE -> {
                    val url = value(2).trim()
                    if (url.isNotEmpty()) acc.websites.add(url.take(MAX_FIELD_CHARS))
                }
                ContactsContract.CommonDataKinds.Event.CONTENT_ITEM_TYPE -> {
                    if (value(3) == ContactsContract.CommonDataKinds.Event.TYPE_BIRTHDAY.toString()) {
                        acc.birthday = value(2).trim().take(MAX_FIELD_CHARS)
                    }
                }
            }
        }
    }

    private val CONTACT_PROJECTION = arrayOf(
        ContactsContract.Contacts._ID,
        ContactsContract.Contacts.DISPLAY_NAME_PRIMARY,
        ContactsContract.Contacts.CONTACT_LAST_UPDATED_TIMESTAMP,
        ContactsContract.Contacts.STARRED,
    )

    // Index map used by readDataRows():
    //   0 CONTACT_ID, 1 MIMETYPE, 2 DATA1, 3 DATA2, 4 DATA3 (label),
    //   5..13 DATA4..DATA12
    private val DATA_PROJECTION = arrayOf(
        ContactsContract.Data.CONTACT_ID,
        ContactsContract.Data.MIMETYPE,
        ContactsContract.Data.DATA1,
        ContactsContract.Data.DATA2,
        ContactsContract.Data.DATA3,
        ContactsContract.Data.DATA4,
        ContactsContract.Data.DATA5,
        ContactsContract.Data.DATA6,
        ContactsContract.Data.DATA7,
        ContactsContract.Data.DATA8,
        ContactsContract.Data.DATA9,
        ContactsContract.Data.DATA10,
        ContactsContract.Data.DATA11,
        ContactsContract.Data.DATA12,
    )
}

/** Reads the SMS inbox/sent store into immutable, content-hashed records. */
object SmsBackupSource {
    private const val TAG = "SmsBackup"
    private const val MAX_BODY_CHARS = 8_000

    fun read(
        context: Context,
        skipIfUnchanged: (String, String) -> Boolean = { _, _ -> false },
    ): ScanResult {
        if (!CommsAccess.canReadSms(context)) {
            return ScanResult(records = emptyList(), complete = false)
        }
        val records = ArrayList<ScannedRecord>()
        val keys = HashSet<String>()
        var complete = true
        var oversized = 0
        var unchanged = 0
        try {
            context.contentResolver.query(
                Telephony.Sms.CONTENT_URI,
                SMS_PROJECTION,
                null,
                null,
                null,
            )?.use { cursor ->
                while (cursor.moveToNext()) {
                    val id = cursor.getLong(0)
                    val address = if (cursor.isNull(1)) "" else cursor.getString(1).orEmpty()
                    val body = (if (cursor.isNull(2)) "" else cursor.getString(2).orEmpty())
                        .take(MAX_BODY_CHARS)
                    val dateMs = if (cursor.isNull(3)) 0L else cursor.getLong(3)
                    val type = smsTypeName(if (cursor.isNull(4)) 0 else cursor.getInt(4))
                    val read = !cursor.isNull(5) && cursor.getInt(5) != 0
                    val threadId = if (cursor.isNull(6)) 0L else cursor.getLong(6)
                    val serviceCenter = if (cursor.isNull(7)) "" else cursor.getString(7).orEmpty()
                    val subject = if (cursor.isNull(8)) "" else cursor.getString(8).orEmpty()
                    if (body.isBlank() && address.isBlank()) continue
                    val key = PersonalBackupPolicy.smsKey(id, dateMs, address)
                    keys.add(key)
                    val payload = PersonalBackupCodec.encodeSms(
                        SmsBackupRecord(
                            address = address,
                            body = body,
                            dateMs = dateMs,
                            type = type,
                            read = read,
                            threadId = threadId,
                            serviceCenter = serviceCenter,
                            subject = subject.take(200),
                        ),
                    )
                    if (!PersonalBackupPolicy.allowsItem(
                            PersonalBackupPolicy.KIND_SMS,
                            payload.toByteArray(Charsets.UTF_8).size,
                        )
                    ) {
                        oversized++
                        continue
                    }
                    val hash = PersonalBackupPolicy.contentHash(payload)
                    if (skipIfUnchanged(key, hash)) {
                        unchanged++
                        continue
                    }
                    records.add(
                        ScannedRecord(
                            key = key,
                            revision = dateMs,
                            hash = hash,
                            dataJson = payload,
                        ),
                    )
                }
            } ?: run { complete = false }
        } catch (e: Exception) {
            Log.w(TAG, "sms query failed: ${e.javaClass.simpleName}")
            complete = false
        }
        return ScanResult(
            records = records,
            complete = complete,
            skippedOversized = oversized,
            unchanged = unchanged,
            keys = keys,
        )
    }

    private fun smsTypeName(type: Int): String = when (type) {
        Telephony.Sms.MESSAGE_TYPE_SENT -> "sent"
        Telephony.Sms.MESSAGE_TYPE_DRAFT -> "draft"
        Telephony.Sms.MESSAGE_TYPE_OUTBOX -> "outbox"
        Telephony.Sms.MESSAGE_TYPE_FAILED -> "failed"
        Telephony.Sms.MESSAGE_TYPE_QUEUED -> "queued"
        else -> "inbox"
    }

    private val SMS_PROJECTION = arrayOf(
        Telephony.Sms._ID,
        Telephony.Sms.ADDRESS,
        Telephony.Sms.BODY,
        Telephony.Sms.DATE,
        Telephony.Sms.TYPE,
        Telephony.Sms.READ,
        Telephony.Sms.THREAD_ID,
        Telephony.Sms.SERVICE_CENTER,
        Telephony.Sms.SUBJECT,
    )
}
