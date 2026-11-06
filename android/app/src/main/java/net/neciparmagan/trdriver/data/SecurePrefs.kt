package net.neciparmagan.trdriver.data

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKeys

/**
 * Keystore-backed preferences. Migrates legacy plaintext preferences once and
 * clears the old file so device tokens are not left behind in cleartext.
 */
object SecurePrefs {
    fun open(context: Context, encryptedName: String, legacyName: String): SharedPreferences {
        val masterKeyAlias = MasterKeys.getOrCreate(MasterKeys.AES256_GCM_SPEC)
        val prefs = EncryptedSharedPreferences.create(
            encryptedName,
            masterKeyAlias,
            context,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
        )
        val legacy = context.getSharedPreferences(legacyName, Context.MODE_PRIVATE)
        val old = legacy.all
        if (old.isNotEmpty()) {
            val editor = prefs.edit()
            for ((key, value) in old) {
                when (value) {
                    is String -> editor.putString(key, value)
                    is Boolean -> editor.putBoolean(key, value)
                    is Int -> editor.putInt(key, value)
                    is Long -> editor.putLong(key, value)
                    is Float -> editor.putFloat(key, value)
                    is Set<*> -> editor.putStringSet(key, value.filterIsInstance<String>().toSet())
                }
            }
            if (editor.commit()) {
                legacy.edit().clear().commit()
            }
        }
        return prefs
    }
}
