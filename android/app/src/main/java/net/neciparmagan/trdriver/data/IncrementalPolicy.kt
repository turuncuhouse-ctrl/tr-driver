package net.neciparmagan.trdriver.data

/**
 * Decides whether an already-catalogued backup item must be uploaded again.
 *
 * Pure logic — JVM unit tested. Rules:
 *  - no record                      → NEW (first backup)
 *  - size changed                   → UPLOAD (file was modified)
 *  - size + mtime unchanged         → SKIP (cheapest common path, no I/O)
 *  - size same, mtime changed       → compare SHA-256 content hashes:
 *                                      equal → SKIP, different → UPLOAD
 *  - legacy row (no hash, no mtime) → ADOPT_META: record what we know and
 *                                      keep skipping — upgrading the app must
 *                                      never re-upload the whole library
 *  - legacy row, mtime known later  → UPLOAD once, then the hash is stored
 */
object IncrementalPolicy {
    enum class Decision { NEW, SKIP, UPLOAD, ADOPT_META }

    /** Stored catalogue state for one media key. */
    data class Stored(
        val sizeBytes: Long,
        val mtimeMs: Long,
        val contentHash: String,
    )

    /**
     * True when size/mtime alone are inconclusive and the caller must read
     * the current content hash before deciding.
     */
    fun needsContentHash(stored: Stored?, currentSize: Long, currentMtimeMs: Long): Boolean {
        if (stored == null) return false
        if (stored.sizeBytes != currentSize) return false
        if (stored.mtimeMs >= 0 && currentMtimeMs >= 0 && stored.mtimeMs == currentMtimeMs) return false
        return stored.contentHash.isNotBlank()
    }

    /**
     * @param currentHash SHA-256 of the current file content, or null when it
     * has not been read yet. It is only required when [needsContentHash] is
     * true; with null the call reports SKIP and the caller re-invokes with the
     * computed hash.
     */
    fun decide(
        stored: Stored?,
        currentSize: Long,
        currentMtimeMs: Long,
        currentHash: String? = null,
    ): Decision {
        if (stored == null) return Decision.NEW
        if (stored.sizeBytes != currentSize) return Decision.UPLOAD
        if (stored.mtimeMs >= 0 && currentMtimeMs >= 0 && stored.mtimeMs == currentMtimeMs) {
            return Decision.SKIP
        }
        if (stored.contentHash.isNotBlank()) {
            if (currentHash == null) return Decision.SKIP
            return if (currentHash.equals(stored.contentHash, ignoreCase = true)) {
                Decision.SKIP
            } else {
                Decision.UPLOAD
            }
        }
        return if (stored.mtimeMs >= 0) Decision.UPLOAD else Decision.ADOPT_META
    }
}
