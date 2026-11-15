package net.neciparmagan.trdriver.calls

import java.util.Locale

data class ContactPhoneEntry(
    val rawNumber: String,
    val label: String = "",
)

data class ContactDirectoryEntry(
    val contactId: Long,
    val displayName: String,
    val starred: Boolean = false,
    val phones: List<ContactPhoneEntry> = emptyList(),
    val sipAddresses: List<String> = emptyList(),
)

enum class MatchConfidence {
    EXACT_E164,
    EXTENSION,
    SIP_URI,
    NATIONAL_SUFFIX,
}

data class ContactMatchResult(
    val contactId: Long,
    val displayName: String,
    val matchedNumber: String,
    val numberLabel: String,
    val normalizedE164: String?,
    val confidence: MatchConfidence,
)

internal data class IndexedCandidate(
    val entry: ContactDirectoryEntry,
    val rawNumber: String,
    val label: String,
    val normalized: NormalizedNumber,
)

class ContactIndex internal constructor(
    private val entries: List<ContactDirectoryEntry>,
    private val byE164: Map<String, List<IndexedCandidate>>,
    private val byExtension: Map<String, List<IndexedCandidate>>,
    private val bySipUser: Map<String, List<IndexedCandidate>>,
    private val byMatchKey: Map<String, List<IndexedCandidate>>,
) {
    fun size(): Int = entries.size

    fun match(rawInput: String?, defaultCountryCode: String = "90"): ContactMatchResult? {
        val query = PhoneNumberNormalizer.normalize(rawInput, defaultCountryCode)
        if (!query.isDialable) return null

        query.e164?.let { e164 ->
            bestCandidate(byE164[e164])?.let { cand ->
                return toResult(cand, MatchConfidence.EXACT_E164)
            }
        }

        query.extensionKey?.let { ext ->
            bestCandidate(byExtension[ext])?.let { cand ->
                return toResult(cand, MatchConfidence.EXTENSION)
            }
        }

        query.sipUser?.let { sip ->
            bestCandidate(bySipUser[sip])?.let { cand ->
                return toResult(cand, MatchConfidence.SIP_URI)
            }
            val bareUser = sip.substringBefore('@')
            if (bareUser.isNotEmpty() && bareUser != sip) {
                bestCandidate(bySipUser[bareUser])?.let { cand ->
                    return toResult(cand, MatchConfidence.SIP_URI)
                }
            }
        }

        query.matchKey?.let { key ->
            val candidates = byMatchKey[key].orEmpty().filter { cand ->
                // Never match across conflicting explicit country codes (+49 vs +90).
                val qcc = query.countryCode
                val ccc = cand.normalized.countryCode
                qcc == null || ccc == null || qcc == ccc
            }
            bestCandidate(candidates)?.let { cand ->
                return toResult(cand, MatchConfidence.NATIONAL_SUFFIX)
            }
        }

        return null
    }

    fun search(query: String, limit: Int = 100): List<ContactDirectoryEntry> {
        val trimmed = query.trim()
        if (trimmed.isEmpty()) return entries.take(limit)
        val lower = normalizeSearchText(trimmed)
        val digitQuery = trimmed.filter { it in '0'..'9' }
        val normalizedQuery = PhoneNumberNormalizer.normalize(trimmed)
        return entries.filter { entry ->
            normalizeSearchText(entry.displayName).contains(lower) ||
                entry.phones.any { p ->
                    val normP = PhoneNumberNormalizer.normalize(p.rawNumber)
                    (digitQuery.isNotEmpty() && p.rawNumber.filter { it in '0'..'9' }.contains(digitQuery)) ||
                        (normalizedQuery.matchKey != null && normP.matchKey == normalizedQuery.matchKey) ||
                        (normalizedQuery.e164 != null && normP.e164 == normalizedQuery.e164)
                } ||
                entry.sipAddresses.any { it.lowercase().contains(lower) }
        }.take(limit)
    }

    private fun bestCandidate(candidates: List<IndexedCandidate>?): IndexedCandidate? {
        if (candidates.isNullOrEmpty()) return null
        return candidates.minWithOrNull(
            compareByDescending<IndexedCandidate> { it.entry.starred }
                .thenByDescending { it.entry.displayName.isNotBlank() }
                .thenBy { it.entry.displayName.lowercase(Locale("tr", "TR")) }
                .thenBy { it.entry.contactId },
        )
    }

    private fun toResult(cand: IndexedCandidate, confidence: MatchConfidence): ContactMatchResult {
        val name = cand.entry.displayName.ifBlank { cand.normalized.displayNumber }
        return ContactMatchResult(
            contactId = cand.entry.contactId,
            displayName = name,
            matchedNumber = cand.rawNumber,
            numberLabel = cand.label,
            normalizedE164 = cand.normalized.e164,
            confidence = confidence,
        )
    }

    private fun normalizeSearchText(text: String): String =
        text.lowercase(Locale("tr", "TR"))
            .replace('ı', 'i')
            .replace('İ', 'i')
            .replace('ğ', 'g')
            .replace('ü', 'u')
            .replace('ş', 's')
            .replace('ö', 'o')
            .replace('ç', 'c')
}

object ContactMatcher {
    fun buildIndex(
        entries: List<ContactDirectoryEntry>,
        defaultCountryCode: String = "90",
    ): ContactIndex {
        val byE164 = HashMap<String, MutableList<IndexedCandidate>>()
        val byExtension = HashMap<String, MutableList<IndexedCandidate>>()
        val bySipUser = HashMap<String, MutableList<IndexedCandidate>>()
        val byMatchKey = HashMap<String, MutableList<IndexedCandidate>>()

        val sortedEntries = entries.sortedWith(
            compareByDescending<ContactDirectoryEntry> { it.starred }
                .thenBy { it.displayName.lowercase(Locale("tr", "TR")) }
                .thenBy { it.contactId },
        )

        for (entry in sortedEntries) {
            for (phone in entry.phones) {
                val norm = PhoneNumberNormalizer.normalize(phone.rawNumber, defaultCountryCode)
                if (!norm.isDialable) continue
                val cand = IndexedCandidate(entry, phone.rawNumber, phone.label, norm)
                norm.e164?.let { byE164.getOrPut(it) { ArrayList() }.add(cand) }
                norm.extensionKey?.let { byExtension.getOrPut(it) { ArrayList() }.add(cand) }
                norm.sipUser?.let { bySipUser.getOrPut(it) { ArrayList() }.add(cand) }
                norm.matchKey?.let { byMatchKey.getOrPut(it) { ArrayList() }.add(cand) }
            }
            for (sip in entry.sipAddresses) {
                val norm = PhoneNumberNormalizer.normalize(sip, defaultCountryCode)
                val sipKey = norm.sipUser ?: sip.trim().lowercase().takeIf { it.isNotEmpty() } ?: continue
                val cand = IndexedCandidate(entry, sip, "LocalTel", norm)
                bySipUser.getOrPut(sipKey) { ArrayList() }.add(cand)
                val bare = sipKey.substringBefore('@')
                if (bare.isNotEmpty() && bare != sipKey) {
                    bySipUser.getOrPut(bare) { ArrayList() }.add(cand)
                }
            }
        }

        return ContactIndex(
            entries = sortedEntries,
            byE164 = byE164,
            byExtension = byExtension,
            bySipUser = bySipUser,
            byMatchKey = byMatchKey,
        )
    }
}
