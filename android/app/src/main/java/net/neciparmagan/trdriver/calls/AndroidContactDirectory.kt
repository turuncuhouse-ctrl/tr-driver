package net.neciparmagan.trdriver.calls

import android.content.Context
import android.provider.ContactsContract
import net.neciparmagan.trdriver.data.CommsAccess

data class ResolvedCallParty(
    val normalized: NormalizedNumber,
    val displayName: String,
    val subtitleNumber: String,
    val contactLabel: String,
    val matchedFromContacts: Boolean,
    val contactId: Long? = null,
)

/**
 * Reads Android contacts using Phase 2 READ_CONTACTS permission and matches
 * incoming/outgoing numbers with [ContactMatcher].
 * Never logs contact names or phone numbers.
 */
object AndroidContactDirectory {
    @Volatile
    private var cachedIndex: ContactIndex? = null

    @Volatile
    private var cachedCountry: String = "90"

    @Volatile
    private var cachedAtMs: Long = 0L

    private const val CACHE_TTL_MS = 60_000L

    fun invalidate() {
        cachedIndex = null
        cachedAtMs = 0L
    }

    fun loadIndex(
        context: Context,
        defaultCountryCode: String = "90",
        forceRefresh: Boolean = false,
    ): ContactIndex {
        if (!CommsAccess.hasContactsAccess(context)) {
            return ContactMatcher.buildIndex(emptyList(), defaultCountryCode)
        }
        val now = System.currentTimeMillis()
        val existing = cachedIndex
        if (!forceRefresh &&
            existing != null &&
            cachedCountry == defaultCountryCode &&
            (now - cachedAtMs) < CACHE_TTL_MS
        ) {
            return existing
        }

        val entries = readEntriesFromProvider(context)
        val rebuilt = ContactMatcher.buildIndex(entries, defaultCountryCode)
        cachedIndex = rebuilt
        cachedCountry = defaultCountryCode
        cachedAtMs = now
        return rebuilt
    }

    fun resolveParty(
        context: Context,
        rawTarget: String,
        fallbackDisplayName: String = "",
        defaultCountryCode: String = "90",
    ): ResolvedCallParty {
        val norm = PhoneNumberNormalizer.normalize(rawTarget, defaultCountryCode)
        val index = loadIndex(context, defaultCountryCode)
        val match = index.match(rawTarget, defaultCountryCode)
        if (match != null) {
            return ResolvedCallParty(
                normalized = norm,
                displayName = match.displayName,
                subtitleNumber = norm.displayNumber.ifBlank { match.matchedNumber },
                contactLabel = match.numberLabel,
                matchedFromContacts = true,
                contactId = match.contactId,
            )
        }
        val fallback = fallbackDisplayName.trim()
            .ifEmpty { norm.displayNameHint.orEmpty() }
            .ifEmpty { norm.displayNumber.ifBlank { rawTarget.trim() } }
        return ResolvedCallParty(
            normalized = norm,
            displayName = fallback,
            subtitleNumber = norm.displayNumber.ifBlank { rawTarget.trim() },
            contactLabel = "",
            matchedFromContacts = false,
            contactId = null,
        )
    }

    private fun readEntriesFromProvider(context: Context): List<ContactDirectoryEntry> {
        val resolver = context.contentResolver
        val names = LinkedHashMap<Long, Pair<String, Boolean>>()
        val phonesByContact = HashMap<Long, MutableList<ContactPhoneEntry>>()
        val sipByContact = HashMap<Long, MutableList<String>>()

        try {
            resolver.query(
                ContactsContract.Contacts.CONTENT_URI,
                arrayOf(
                    ContactsContract.Contacts._ID,
                    ContactsContract.Contacts.DISPLAY_NAME_PRIMARY,
                    ContactsContract.Contacts.STARRED,
                ),
                null,
                null,
                "${ContactsContract.Contacts.STARRED} DESC, ${ContactsContract.Contacts.DISPLAY_NAME_PRIMARY} ASC",
            )?.use { c ->
                val idCol = c.getColumnIndex(ContactsContract.Contacts._ID)
                val nameCol = c.getColumnIndex(ContactsContract.Contacts.DISPLAY_NAME_PRIMARY)
                val starCol = c.getColumnIndex(ContactsContract.Contacts.STARRED)
                while (c.moveToNext()) {
                    val id = if (idCol >= 0) c.getLong(idCol) else continue
                    val name = if (nameCol >= 0) c.getString(nameCol).orEmpty().trim() else ""
                    val starred = starCol >= 0 && c.getInt(starCol) == 1
                    names[id] = name to starred
                }
            }

            resolver.query(
                ContactsContract.CommonDataKinds.Phone.CONTENT_URI,
                arrayOf(
                    ContactsContract.CommonDataKinds.Phone.CONTACT_ID,
                    ContactsContract.CommonDataKinds.Phone.NUMBER,
                    ContactsContract.CommonDataKinds.Phone.TYPE,
                    ContactsContract.CommonDataKinds.Phone.LABEL,
                ),
                null,
                null,
                null,
            )?.use { c ->
                val cidCol = c.getColumnIndex(ContactsContract.CommonDataKinds.Phone.CONTACT_ID)
                val numCol = c.getColumnIndex(ContactsContract.CommonDataKinds.Phone.NUMBER)
                val typeCol = c.getColumnIndex(ContactsContract.CommonDataKinds.Phone.TYPE)
                val labelCol = c.getColumnIndex(ContactsContract.CommonDataKinds.Phone.LABEL)
                while (c.moveToNext()) {
                    val cid = if (cidCol >= 0) c.getLong(cidCol) else continue
                    val num = if (numCol >= 0) c.getString(numCol).orEmpty().trim() else ""
                    if (num.isEmpty()) continue
                    val type = if (typeCol >= 0) c.getInt(typeCol) else ContactsContract.CommonDataKinds.Phone.TYPE_OTHER
                    val custom = if (labelCol >= 0) c.getString(labelCol) else null
                    val label = ContactsContract.CommonDataKinds.Phone.getTypeLabel(
                        context.resources,
                        type,
                        custom,
                    ).toString()
                    phonesByContact.getOrPut(cid) { ArrayList() }.add(ContactPhoneEntry(num, label))
                }
            }

            resolver.query(
                ContactsContract.Data.CONTENT_URI,
                arrayOf(
                    ContactsContract.Data.CONTACT_ID,
                    ContactsContract.CommonDataKinds.SipAddress.SIP_ADDRESS,
                ),
                "${ContactsContract.Data.MIMETYPE} = ?",
                arrayOf(ContactsContract.CommonDataKinds.SipAddress.CONTENT_ITEM_TYPE),
                null,
            )?.use { c ->
                val cidCol = c.getColumnIndex(ContactsContract.Data.CONTACT_ID)
                val sipCol = c.getColumnIndex(ContactsContract.CommonDataKinds.SipAddress.SIP_ADDRESS)
                while (c.moveToNext()) {
                    val cid = if (cidCol >= 0) c.getLong(cidCol) else continue
                    val sip = if (sipCol >= 0) c.getString(sipCol).orEmpty().trim() else ""
                    if (sip.isNotEmpty()) {
                        sipByContact.getOrPut(cid) { ArrayList() }.add(sip)
                    }
                }
            }
        } catch (_: SecurityException) {
            return emptyList()
        } catch (_: Throwable) {
            // Fall through with whatever was read.
        }

        val result = ArrayList<ContactDirectoryEntry>(names.size)
        for ((id, meta) in names) {
            val phones = phonesByContact[id].orEmpty()
            val sips = sipByContact[id].orEmpty()
            if (phones.isEmpty() && sips.isEmpty()) continue
            result.add(
                ContactDirectoryEntry(
                    contactId = id,
                    displayName = meta.first,
                    starred = meta.second,
                    phones = phones,
                    sipAddresses = sips,
                ),
            )
        }
        return result
    }
}
