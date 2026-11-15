package net.neciparmagan.trdriver.data

import java.io.File
import java.io.InputStream
import java.security.MessageDigest

/**
 * SHA-256 helpers shared by update-APK verification and incremental backup
 * change detection. Pure JVM — unit tested.
 */
object ContentHasher {
    private const val HEX = "0123456789ABCDEF"

    fun sha256Hex(input: InputStream): String {
        val md = MessageDigest.getInstance("SHA-256")
        val buf = ByteArray(64 * 1024)
        while (true) {
            val n = input.read(buf)
            if (n < 0) break
            if (n > 0) md.update(buf, 0, n)
        }
        return toHex(md.digest())
    }

    fun sha256Hex(file: File): String = file.inputStream().use { sha256Hex(it) }

    /**
     * A blank expected value (older manifest without apkSha256) means there is
     * nothing to compare — the caller falls back to the signature pin only.
     */
    fun matches(actualHex: String, expectedHex: String): Boolean {
        val expected = expectedHex.trim()
        if (expected.isEmpty()) return true
        return actualHex.trim().equals(expected, ignoreCase = true)
    }

    private fun toHex(bytes: ByteArray): String {
        val out = StringBuilder(bytes.size * 2)
        for (b in bytes) {
            val v = b.toInt() and 0xFF
            out.append(HEX[v ushr 4])
            out.append(HEX[v and 0x0F])
        }
        return out.toString()
    }
}
