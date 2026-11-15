package net.neciparmagan.trdriver.data

import java.security.MessageDigest
import java.util.Base64

/**
 * Pure rules for the contacts / SMS insurance backup.
 *
 * Framework-free so every rule (stable keys, incremental decisions, deletion
 * safety, batching, payload limits) is covered by JVM unit tests. The server
 * side of the same contract lives in `internal/personalbackup`.
 */
object PersonalBackupPolicy {
    const val KIND_CONTACTS = "contacts"
    const val KIND_SMS = "sms"

    /** Rows per POST. Small enough to keep one request cheap on mobile Wi‑Fi. */
    const val CONTACTS_PER_REQUEST = 200
    const val SMS_PER_REQUEST = 100

    /** Must stay below the server caps (16 KiB contacts, 64 KiB per SMS). */
    const val MAX_CONTACT_BYTES = 16 * 1024
    const val MAX_SMS_BYTES = 64 * 1024

    /** Client keys are 43-char base64url SHA-256 digests (server allows 8..128). */
    const val KEY_LENGTH = 43

    enum class Decision { NEW, UPDATE, SKIP }

    /** What the device already pushed for one item key. */
    data class Stored(val revision: Long, val hash: String)

    /**
     * Incremental decision, mirroring the server:
     * identical hash => nothing to do; an older revision than the one already
     * stored => the server keeps the newer row, so skip instead of wasting a
     * request that would be rejected as stale.
     */
    fun decide(stored: Stored?, revision: Long, hash: String): Decision = when {
        stored == null -> Decision.NEW
        stored.hash.isNotEmpty() && stored.hash == hash -> Decision.SKIP
        revision > 0L && stored.revision > revision -> Decision.SKIP
        else -> Decision.UPDATE
    }

    /**
     * Keys that disappeared from the phone since the last push.
     *
     * Safety rules: never delete when the scan was partial, when the provider
     * returned nothing (revoked permission / empty SIM) and never invent keys
     * that were not stored locally.
     */
    fun pendingDeletions(
        storedKeys: Collection<String>,
        scannedKeys: Set<String>,
        scanComplete: Boolean,
    ): List<String> {
        if (!scanComplete) return emptyList()
        if (storedKeys.isEmpty() || scannedKeys.isEmpty()) return emptyList()
        return storedKeys.filter { it !in scannedKeys }.sorted()
    }

    fun chunkSize(kind: String): Int =
        if (kind == KIND_SMS) SMS_PER_REQUEST else CONTACTS_PER_REQUEST

    fun maxItemBytes(kind: String): Int =
        if (kind == KIND_SMS) MAX_SMS_BYTES else MAX_CONTACT_BYTES

    fun allowsItem(kind: String, bytes: Int): Boolean = bytes in 1..maxItemBytes(kind)

    fun <T> chunk(items: List<T>, size: Int): List<List<T>> {
        if (items.isEmpty()) return emptyList()
        val effective = size.coerceAtLeast(1)
        val out = ArrayList<List<T>>((items.size + effective - 1) / effective)
        var index = 0
        while (index < items.size) {
            val end = (index + effective).coerceAtMost(items.size)
            out.add(items.subList(index, end))
            index = end
        }
        return out
    }

    // --- identity -----------------------------------------------------------

    /**
     * Last ten digits of a number, ignoring formatting and the country/trunk
     * prefix, so "+90 (555) 111 22 33" and "0555 111 22 33" are one identity.
     * Returns null for blanks and short codes.
     */
    fun phoneIdentity(raw: String?): String? {
        if (raw.isNullOrBlank()) return null
        val digits = raw.filter { it in '0'..'9' }
        if (digits.length < 7) return null
        return digits.takeLast(10)
    }

    /**
     * Stable identity for one contact: primary phone first, then e-mail, then
     * the normalised display name. Two contacts that share a number collapse
     * into one row; that is the same trade-off phone address books make.
     */
    fun contactIdentity(displayName: String, phones: List<String>, emails: List<String>): String {
        val numbers = phones.mapNotNull { phoneIdentity(it) }.distinct().sorted()
        if (numbers.isNotEmpty()) return "p:" + numbers.first()
        val mails = emails.map { it.trim().lowercase() }
            .filter { it.contains('@') }
            .distinct()
            .sorted()
        if (mails.isNotEmpty()) return "e:" + mails.first()
        return "n:" + normalizeName(displayName)
    }

    fun contactKey(displayName: String, phones: List<String>, emails: List<String>): String =
        hashKey("contact|" + contactIdentity(displayName, phones, emails))

    /** SMS rows are immutable: provider id + date + address identify one message. */
    fun smsIdentity(id: Long, dateMs: Long, address: String): String {
        val who = phoneIdentity(address) ?: normalizeName(address)
        return "sms|$id|$dateMs|$who"
    }

    fun smsKey(id: Long, dateMs: Long, address: String): String =
        hashKey(smsIdentity(id, dateMs, address))

    fun normalizeName(raw: String): String =
        raw.trim().lowercase().replace(Regex("\\s+"), " ")

    /** 43-char base64url SHA-256 digest: no padding, URL safe, server accepted. */
    fun hashKey(identity: String): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(identity.toByteArray(Charsets.UTF_8))
        return Base64.getUrlEncoder().withoutPadding().encodeToString(digest)
    }

    /** Hex SHA-256 used as the content hash both sides compare. */
    fun contentHash(payload: String): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(payload.toByteArray(Charsets.UTF_8))
        val out = StringBuilder(digest.size * 2)
        for (byte in digest) {
            val value = byte.toInt() and 0xff
            out.append(HEX[value ushr 4]).append(HEX[value and 0x0f])
        }
        return out.toString()
    }

    fun isSafeKey(key: String): Boolean =
        key.length == KEY_LENGTH &&
            key.all { it in 'a'..'z' || it in 'A'..'Z' || it in '0'..'9' || it == '-' || it == '_' }

    private val HEX = "0123456789abcdef".toCharArray()
}
