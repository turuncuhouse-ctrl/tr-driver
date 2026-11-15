package net.neciparmagan.trdriver.data

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.ContextCompat

/**
 * Runtime access for the personal insurance backup (contacts + SMS).
 *
 * Both permissions are requested only after the explanation dialog in
 * Backup settings; nothing is read while the matching switch is off.
 */
object CommsAccess {
    fun contactsPermission(): String = Manifest.permission.READ_CONTACTS

    fun smsPermission(): String = Manifest.permission.READ_SMS

    fun contactsPermissionsForRequest(): Array<String> = arrayOf(Manifest.permission.READ_CONTACTS)

    fun smsPermissionsForRequest(): Array<String> = arrayOf(Manifest.permission.READ_SMS)

    fun hasContactsAccess(context: Context): Boolean =
        granted(context, Manifest.permission.READ_CONTACTS)

    fun hasSmsAccess(context: Context): Boolean =
        granted(context, Manifest.permission.READ_SMS)

    /**
     * SMS access additionally needs the telephony provider to be readable.
     * A device without telephony (tablet/emulator) simply reports false.
     */
    fun canReadSms(context: Context): Boolean {
        if (!hasSmsAccess(context)) return false
        if (!context.packageManager.hasSystemFeature(PackageManager.FEATURE_TELEPHONY)) {
            // Multi-SIM tablets can still expose the provider; probe it cheaply.
            return runCatching {
                context.contentResolver.query(
                    android.provider.Telephony.Sms.CONTENT_URI,
                    arrayOf(android.provider.Telephony.Sms._ID),
                    null,
                    null,
                    null,
                )?.use { it.moveToFirst() } ?: false
            }.getOrDefault(false)
        }
        return true
    }

    /** Deep link to the OS permission screen for this app. */
    fun openAppSettings(context: Context) {
        val intent = android.content.Intent(
            android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
        ).apply {
            data = android.net.Uri.parse("package:${context.packageName}")
            addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        runCatching { context.startActivity(intent) }
    }

    /** Explanation text shown before the system prompt (Turkish, as the app UI). */
    fun contactsExplanation(): String = buildString {
        append("Rehberiniz, telefon kaybolursa geri yükleyebilmeniz için sunucuya yedeklenir.\n\n")
        append("• Yalnızca Wi‑Fi bağlıyken ve uygulama arka plandayken çalışır.\n")
        append("• Ad, telefon numarası, e‑posta ve adres alanları sunucuda şifreli saklanır.\n")
        append("• Yedekleme yalnızca bu cihazın hesabına yüklenir; başka kullanıcı göremez.\n")
        append("• İzin: READ_CONTACTS. İstediğiniz an anahtarı kapatıp izni geri alabilirsiniz.\n\n")
        append("Devam etmek için Android izin penceresinde “İzin ver” seçin.")
    }

    fun smsExplanation(): String = buildString {
        append("SMS mesajlarınız, telefon kaybolursa geri yükleyebilmeniz için sunucuya yedeklenir.\n\n")
        append("• Yalnızca Wi‑Fi bağlıyken ve uygulama arka plandayken çalışır.\n")
        append("• Numara, mesaj metni ve tarih sunucuda şifreli saklanır.\n")
        append("• Yedek yalnızca bu cihazın hesabına yüklenir; başka kullanıcı göremez.\n")
        append("• İzin: READ_SMS. İstediğiniz an anahtarı kapatıp izni geri alabilirsiniz.\n\n")
        append("Devam etmek için Android izin penceresinde “İzin ver” seçin.")
    }

    private fun granted(context: Context, permission: String): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return true
        return ContextCompat.checkSelfPermission(context, permission) ==
            PackageManager.PERMISSION_GRANTED
    }
}
