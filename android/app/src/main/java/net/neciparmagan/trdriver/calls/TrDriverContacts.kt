package net.neciparmagan.trdriver.calls

import java.security.MessageDigest

/**
 * Address-book <-> TR Driver member matching (WhatsApp style). Contact numbers
 * are compared on their last 10 digits and only sent to the server as SHA-256
 * hashes; the server answers with members whose registered phone matches.
 */
object TrDriverContacts {
    @Volatile
    private var byHash: Map<String, LocalTelConfigApi.DirectoryMember> = emptyMap()

    /** sha256(last 10 digits) or null when the number is too short to be a phone. */
    fun hashFor(raw: String?): String? {
        val digits = raw.orEmpty().filter { it in '0'..'9' }
        if (digits.length < 10) return null
        val tail = digits.takeLast(10)
        return MessageDigest.getInstance("SHA-256").digest(tail.toByteArray())
            .joinToString("") { "%02x".format(it) }
    }

    fun memberFor(raw: String?): LocalTelConfigApi.DirectoryMember? = hashFor(raw)?.let { byHash[it] }

    fun isMember(raw: String?): Boolean = memberFor(raw) != null

    /** Re-matches the whole address book; call after every contact (re)load. */
    suspend fun sync(api: LocalTelConfigApi, index: ContactIndex) {
        val hashes = index.allPhoneNumbers().mapNotNull { hashFor(it) }.distinct()
        if (hashes.isEmpty()) {
            byHash = emptyMap()
            return
        }
        val merged = HashMap<String, LocalTelConfigApi.DirectoryMember>()
        for (chunk in hashes.chunked(2000)) {
            api.matchContacts(chunk).onSuccess { list -> list.forEach { (h, m) -> merged[h] = m } }
                .onFailure { return }
        }
        byHash = merged
    }

    fun remember(raw: String?, member: LocalTelConfigApi.DirectoryMember) {
        val h = hashFor(raw) ?: return
        byHash = byHash + (h to member)
    }
}
