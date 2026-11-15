package net.neciparmagan.trdriver.calls

/**
 * Normalizes phone numbers, extensions and SIP URIs into comparable keys and
 * human-readable display strings without leaking numbers into logs.
 */
data class NormalizedNumber(
    val raw: String,
    val displayNumber: String,
    val e164: String? = null,
    val matchKey: String? = null,
    val extensionKey: String? = null,
    val sipUser: String? = null,
    val countryCode: String? = null,
    val displayNameHint: String? = null,
) {
    val isDialable: Boolean
        get() = e164 != null || matchKey != null || extensionKey != null || sipUser != null
}

object PhoneNumberNormalizer {
    private val KNOWN_TWO_DIGIT_CC = setOf(
        "20", "27", "30", "31", "32", "33", "34", "36", "39",
        "40", "41", "43", "44", "45", "46", "47", "48", "49",
        "51", "52", "53", "54", "55", "56", "57", "58",
        "60", "61", "62", "63", "64", "65", "66",
        "81", "82", "84", "86",
        "90", "91", "92", "93", "94", "95", "98",
    )

    private val KNOWN_ONE_DIGIT_CC = setOf("1", "7")

    fun normalize(raw: String?, defaultCountryCode: String = "90"): NormalizedNumber {
        val original = raw.orEmpty().trim()
        if (original.isEmpty()) {
            return NormalizedNumber(raw = "", displayNumber = "")
        }
        val cleanDefaultCc = defaultCountryCode.filter { it in '0'..'9' }.ifEmpty { "90" }

        var displayHint: String? = null
        var addrPart = original
        val lt = original.indexOf('<')
        val gt = original.lastIndexOf('>')
        if (lt >= 0 && gt > lt) {
            val prefixName = original.substring(0, lt).trim().trim('"').trim()
            if (prefixName.isNotEmpty() && prefixName.length <= 80) {
                displayHint = prefixName
            }
            addrPart = original.substring(lt + 1, gt).trim()
        }

        // Strip URI parameters (?headers / ;user=phone)
        val semiIdx = addrPart.indexOf(';')
        if (semiIdx >= 0) addrPart = addrPart.substring(0, semiIdx)
        val qIdx = addrPart.indexOf('?')
        if (qIdx >= 0) addrPart = addrPart.substring(0, qIdx)

        val lowerAddr = addrPart.lowercase()
        val hasSipScheme = lowerAddr.startsWith("sip:") || lowerAddr.startsWith("sips:")
        addrPart = when {
            lowerAddr.startsWith("sips:") -> addrPart.substring(5)
            lowerAddr.startsWith("sip:") -> addrPart.substring(4)
            lowerAddr.startsWith("tel:") -> addrPart.substring(4)
            else -> addrPart
        }.trim()

        var sipHost = ""
        val atIdx = addrPart.indexOf('@')
        val userPart = if (atIdx >= 0) {
            sipHost = addrPart.substring(atIdx + 1).substringBefore(':').trim().lowercase()
            addrPart.substring(0, atIdx).trim()
        } else {
            addrPart
        }

        val hasLetters = userPart.any { it in 'a'..'z' || it in 'A'..'Z' }
        if (hasLetters || (hasSipScheme && userPart.none { it in '0'..'9' })) {
            val fullSip = if (sipHost.isNotEmpty()) "${userPart.lowercase()}@$sipHost" else userPart.lowercase()
            return NormalizedNumber(
                raw = original,
                displayNumber = if (sipHost.isNotEmpty()) "$userPart@$sipHost" else userPart,
                sipUser = fullSip.takeIf { it.isNotBlank() },
                displayNameHint = displayHint,
            )
        }

        // Phone / extension normalization.
        val stripped = buildString {
            for (ch in userPart) {
                when {
                    ch in '0'..'9' -> append(ch)
                    ch == '+' && isEmpty() -> append('+')
                }
            }
        }
        if (stripped.isEmpty() || stripped == "+") {
            return NormalizedNumber(raw = original, displayNumber = original, displayNameHint = displayHint)
        }

        val explicitPlus = stripped.startsWith("+")
        val digitsRaw = if (explicitPlus) stripped.substring(1) else stripped
        if (digitsRaw.isEmpty()) {
            return NormalizedNumber(raw = original, displayNumber = original, displayNameHint = displayHint)
        }

        val intlDigits = when {
            explicitPlus -> digitsRaw
            digitsRaw.startsWith("00") && digitsRaw.length >= 9 -> digitsRaw.substring(2)
            else -> null
        }

        if (intlDigits != null) {
            val cc = extractCountryCode(intlDigits)
            val national = if (cc != null) intlDigits.removePrefix(cc) else intlDigits
            val matchKey = when {
                national.length >= 10 -> national.takeLast(10)
                national.length >= 7 -> national
                intlDigits.length >= 7 -> intlDigits.takeLast(10)
                else -> null
            }
            val e164 = if (intlDigits.length >= 7) "+$intlDigits" else null
            return NormalizedNumber(
                raw = original,
                displayNumber = formatDisplayNumber(e164, matchKey, null, original),
                e164 = e164,
                matchKey = matchKey,
                extensionKey = if (intlDigits.length in 2..6) intlDigits else null,
                countryCode = cc,
                displayNameHint = displayHint,
            )
        }

        // No explicit international prefix (+ or 00).
        val len = digitsRaw.length
        return when {
            // Short PBX extension (2..6 digits), e.g. "101", "1001", "112".
            len in 2..6 -> NormalizedNumber(
                raw = original,
                displayNumber = digitsRaw,
                extensionKey = digitsRaw,
                displayNameHint = displayHint,
            )
            // 7..9 digits without trunk/country (e.g. "4440123" or local landline).
            len in 7..9 && !digitsRaw.startsWith("0") -> NormalizedNumber(
                raw = original,
                displayNumber = formatSevenToNine(digitsRaw),
                matchKey = digitsRaw,
                displayNameHint = displayHint,
            )
            // National number with leading 0 trunk (e.g. "05551112233" or "02321234567").
            digitsRaw.startsWith("0") && len >= 10 -> {
                val national = digitsRaw.substring(1)
                val e164 = "+$cleanDefaultCc$national"
                NormalizedNumber(
                    raw = original,
                    displayNumber = formatDisplayNumber(e164, national.takeLast(10), null, original),
                    e164 = e164,
                    matchKey = national.takeLast(10),
                    countryCode = cleanDefaultCc,
                    displayNameHint = displayHint,
                )
            }
            // Bare 10-digit national number (e.g. "5551112233").
            len == 10 -> {
                val e164 = "+$cleanDefaultCc$digitsRaw"
                NormalizedNumber(
                    raw = original,
                    displayNumber = formatDisplayNumber(e164, digitsRaw, null, original),
                    e164 = e164,
                    matchKey = digitsRaw,
                    countryCode = cleanDefaultCc,
                    displayNameHint = displayHint,
                )
            }
            // National number prefixed with defaultCountryCode without '+' (e.g. "905551112233").
            digitsRaw.startsWith(cleanDefaultCc) && len == cleanDefaultCc.length + 10 -> {
                val national = digitsRaw.substring(cleanDefaultCc.length)
                val e164 = "+$digitsRaw"
                NormalizedNumber(
                    raw = original,
                    displayNumber = formatDisplayNumber(e164, national, null, original),
                    e164 = e164,
                    matchKey = national,
                    countryCode = cleanDefaultCc,
                    displayNameHint = displayHint,
                )
            }
            else -> {
                val cc = extractCountryCode(digitsRaw)
                val matchKey = digitsRaw.takeLast(10)
                NormalizedNumber(
                    raw = original,
                    displayNumber = "+$digitsRaw",
                    e164 = "+$digitsRaw",
                    matchKey = matchKey,
                    countryCode = cc,
                    displayNameHint = displayHint,
                )
            }
        }
    }

    fun formatForDisplay(raw: String?, defaultCountryCode: String = "90"): String {
        val norm = normalize(raw, defaultCountryCode)
        return norm.displayNumber.ifBlank { raw.orEmpty().trim() }
    }

    private fun extractCountryCode(intlDigits: String): String? {
        if (intlDigits.length < 7) return null
        val one = intlDigits.substring(0, 1)
        if (one in KNOWN_ONE_DIGIT_CC) return one
        if (intlDigits.length >= 2) {
            val two = intlDigits.substring(0, 2)
            if (two in KNOWN_TWO_DIGIT_CC) return two
        }
        return if (intlDigits.length >= 9) intlDigits.substring(0, 3) else null
    }

    private fun formatDisplayNumber(
        e164: String?,
        matchKey: String?,
        extensionKey: String?,
        fallback: String,
    ): String {
        if (extensionKey != null) return extensionKey
        if (e164 != null && e164.startsWith("+90") && e164.length == 13) {
            val area = e164.substring(3, 6)
            val p1 = e164.substring(6, 9)
            val p2 = e164.substring(9, 11)
            val p3 = e164.substring(11, 13)
            return "+90 $area $p1 $p2 $p3"
        }
        if (e164 != null && e164.startsWith("+1") && e164.length == 12) {
            val area = e164.substring(2, 5)
            val p1 = e164.substring(5, 8)
            val p2 = e164.substring(8, 12)
            return "+1 ($area) $p1-$p2"
        }
        return e164 ?: matchKey ?: fallback
    }

    private fun formatSevenToNine(digits: String): String {
        return if (digits.length == 7) {
            "${digits.substring(0, 3)} ${digits.substring(3, 5)} ${digits.substring(5, 7)}"
        } else {
            digits
        }
    }
}
