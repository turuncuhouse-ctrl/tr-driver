package net.neciparmagan.trdriver.backup

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.provider.CallLog
import android.provider.Telephony
import android.util.Log
import androidx.core.content.ContextCompat
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadNetworkGate
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Backs up device SMS and call logs (own phone only) to
 * TR Backup / {device} / SMS|AramaKayitlari as CSV.
 *
 * Cannot read WhatsApp / Telegram / Signal — those apps do not expose messages.
 */
object CommsBackupEngine {
    private const val TAG = "CommsBackup"
    private const val MAX_SMS = 8000
    private const val MAX_CALLS = 5000

    data class Result(val smsOk: Boolean, val callsOk: Boolean, val message: String)

    fun hasSmsPermission(context: Context): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.READ_SMS) ==
            PackageManager.PERMISSION_GRANTED

    fun hasCallLogPermission(context: Context): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.READ_CALL_LOG) ==
            PackageManager.PERMISSION_GRANTED

    suspend fun runIfNeeded(context: Context): Result = withContext(Dispatchers.IO) {
        val app = context.applicationContext
        val session = SessionStore(app)
        if (!session.isLoggedIn) {
            return@withContext Result(false, false, "Oturum yok")
        }
        if (!session.smsBackupEnabled && !session.callLogBackupEnabled) {
            return@withContext Result(false, false, "SMS/arama yedeği kapalı")
        }
        if (!UploadNetworkGate.isWifi(app)) {
            return@withContext Result(false, false, "Wi‑Fi gerekli")
        }
        val api = DriveApi(session, app)
        val parts = mutableListOf<String>()
        var smsOk = false
        var callsOk = false

        if (session.smsBackupEnabled) {
            if (!hasSmsPermission(app)) {
                parts += "SMS izni yok"
            } else {
                try {
                    val csv = exportSmsCsv(app)
                    val folder = api.ensureBackupFolder("SMS")
                    val name = "sms_" + dayStamp() + ".csv"
                    api.uploadTextFile(folder, name, csv, conflict = "overwrite")
                    // Also keep a rolling latest file for easy open
                    api.uploadTextFile(folder, "sms_son.csv", csv, conflict = "overwrite")
                    smsOk = true
                    parts += "SMS yedeklendi"
                } catch (e: Exception) {
                    Log.e(TAG, "SMS backup failed", e)
                    parts += "SMS hata: ${e.message}"
                }
            }
        }

        if (session.callLogBackupEnabled) {
            if (!hasCallLogPermission(app)) {
                parts += "Arama kaydı izni yok"
            } else {
                try {
                    val csv = exportCallLogCsv(app)
                    val folder = api.ensureBackupFolder("AramaKayitlari")
                    val name = "aramalar_" + dayStamp() + ".csv"
                    api.uploadTextFile(folder, name, csv, conflict = "overwrite")
                    api.uploadTextFile(folder, "aramalar_son.csv", csv, conflict = "overwrite")
                    callsOk = true
                    parts += "Arama kayıtları yedeklendi"
                } catch (e: Exception) {
                    Log.e(TAG, "Call log backup failed", e)
                    parts += "Arama hata: ${e.message}"
                }
            }
        }

        val msg = parts.joinToString(" · ").ifBlank { "İletişim yedeği atlandı" }
        if (smsOk || callsOk) {
            session.lastBackupMessage = msg
        }
        Result(smsOk, callsOk, msg)
    }

    private fun dayStamp(): String =
        SimpleDateFormat("yyyy-MM-dd", Locale.US).format(Date())

    private fun exportSmsCsv(context: Context): String {
        val df = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale("tr", "TR"))
        val sb = StringBuilder()
        sb.appendLine("tarih;yon;numara;metin")
        val uri = Telephony.Sms.CONTENT_URI
        val projection = arrayOf(
            Telephony.Sms.DATE,
            Telephony.Sms.TYPE,
            Telephony.Sms.ADDRESS,
            Telephony.Sms.BODY,
        )
        context.contentResolver.query(
            uri,
            projection,
            null,
            null,
            "${Telephony.Sms.DATE} DESC",
        )?.use { c ->
            val iDate = c.getColumnIndex(Telephony.Sms.DATE)
            val iType = c.getColumnIndex(Telephony.Sms.TYPE)
            val iAddr = c.getColumnIndex(Telephony.Sms.ADDRESS)
            val iBody = c.getColumnIndex(Telephony.Sms.BODY)
            var n = 0
            while (c.moveToNext() && n < MAX_SMS) {
                n++
                val date = if (iDate >= 0) c.getLong(iDate) else 0L
                val type = if (iType >= 0) c.getInt(iType) else 0
                val addr = if (iAddr >= 0) c.getString(iAddr).orEmpty() else ""
                val body = if (iBody >= 0) c.getString(iBody).orEmpty() else ""
                val yon = when (type) {
                    Telephony.Sms.MESSAGE_TYPE_INBOX -> "gelen"
                    Telephony.Sms.MESSAGE_TYPE_SENT -> "giden"
                    Telephony.Sms.MESSAGE_TYPE_DRAFT -> "taslak"
                    Telephony.Sms.MESSAGE_TYPE_OUTBOX -> "giden_kutusu"
                    else -> "diger_$type"
                }
                sb.append(df.format(Date(date))).append(';')
                sb.append(yon).append(';')
                sb.append(csvEscape(addr)).append(';')
                sb.append(csvEscape(body))
                sb.appendLine()
            }
        }
        return sb.toString()
    }

    private fun exportCallLogCsv(context: Context): String {
        val df = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale("tr", "TR"))
        val sb = StringBuilder()
        sb.appendLine("tarih;tip;numara;isim;sure_sn")
        val projection = arrayOf(
            CallLog.Calls.DATE,
            CallLog.Calls.TYPE,
            CallLog.Calls.NUMBER,
            CallLog.Calls.CACHED_NAME,
            CallLog.Calls.DURATION,
        )
        context.contentResolver.query(
            CallLog.Calls.CONTENT_URI,
            projection,
            null,
            null,
            "${CallLog.Calls.DATE} DESC",
        )?.use { c ->
            val iDate = c.getColumnIndex(CallLog.Calls.DATE)
            val iType = c.getColumnIndex(CallLog.Calls.TYPE)
            val iNum = c.getColumnIndex(CallLog.Calls.NUMBER)
            val iName = c.getColumnIndex(CallLog.Calls.CACHED_NAME)
            val iDur = c.getColumnIndex(CallLog.Calls.DURATION)
            var n = 0
            while (c.moveToNext() && n < MAX_CALLS) {
                n++
                val date = if (iDate >= 0) c.getLong(iDate) else 0L
                val type = if (iType >= 0) c.getInt(iType) else 0
                val num = if (iNum >= 0) c.getString(iNum).orEmpty() else ""
                val name = if (iName >= 0) c.getString(iName).orEmpty() else ""
                val dur = if (iDur >= 0) c.getLong(iDur) else 0L
                val tip = when (type) {
                    CallLog.Calls.INCOMING_TYPE -> "gelen"
                    CallLog.Calls.OUTGOING_TYPE -> "giden"
                    CallLog.Calls.MISSED_TYPE -> "cevaplanmadi"
                    CallLog.Calls.REJECTED_TYPE -> "reddedildi"
                    CallLog.Calls.BLOCKED_TYPE -> "engelli"
                    else -> "diger_$type"
                }
                sb.append(df.format(Date(date))).append(';')
                sb.append(tip).append(';')
                sb.append(csvEscape(num)).append(';')
                sb.append(csvEscape(name)).append(';')
                sb.append(dur)
                sb.appendLine()
            }
        }
        return sb.toString()
    }

    private fun csvEscape(raw: String): String {
        val t = raw.replace("\r", " ").replace("\n", " ").replace(";", ",")
        return if (t.contains('"')) "\"${t.replace("\"", "\"\"")}\"" else t
    }
}
