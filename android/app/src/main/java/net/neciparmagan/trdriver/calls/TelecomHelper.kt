package net.neciparmagan.trdriver.calls

import android.annotation.SuppressLint
import android.content.ComponentName
import android.content.Context
import android.content.pm.PackageManager
import android.graphics.drawable.Icon
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.telecom.DisconnectCause
import android.telecom.PhoneAccount
import android.telecom.PhoneAccountHandle
import android.telecom.TelecomManager
import net.neciparmagan.trdriver.R
import java.util.concurrent.ConcurrentHashMap

/**
 * Registers and drives the Android Telecom self-managed [PhoneAccount] so
 * LocalTel calls participate cleanly with system call audio, Bluetooth, and
 * carrier call interruption rules without replacing normal GSM calls.
 */
object TelecomHelper {
    private const val ACCOUNT_ID = "trdriver_localtel_v1"
    const val EXTRA_CALL_ID = "tr_call_id"
    const val EXTRA_DISPLAY_NAME = "tr_display_name"
    const val EXTRA_RAW_ADDRESS = "tr_raw_address"

    private val connections = ConcurrentHashMap<String, TrCallConnection>()

    fun phoneAccountHandle(context: Context): PhoneAccountHandle {
        val cn = ComponentName(context.applicationContext, TrConnectionService::class.java)
        return PhoneAccountHandle(cn, ACCOUNT_ID)
    }

    fun isTelecomSupported(context: Context): Boolean {
        val pm = context.packageManager
        val hasTelecom = pm.hasSystemFeature(PackageManager.FEATURE_TELECOM) ||
            pm.hasSystemFeature(PackageManager.FEATURE_CONNECTION_SERVICE)
        return hasTelecom && context.getSystemService(TelecomManager::class.java) != null
    }

    fun ensurePhoneAccount(context: Context): PhoneAccountHandle? {
        if (!isTelecomSupported(context)) return null
        val tm = context.getSystemService(TelecomManager::class.java) ?: return null
        val handle = phoneAccountHandle(context)
        runCatching {
            val builder = PhoneAccount.builder(handle, "TR Driver LocalTel")
                .setCapabilities(PhoneAccount.CAPABILITY_SELF_MANAGED)
                .setIcon(Icon.createWithResource(context, R.drawable.ic_stat_call))
                .addSupportedUriScheme(PhoneAccount.SCHEME_TEL)
                .addSupportedUriScheme(PhoneAccount.SCHEME_SIP)
                .setShortDescription("TR Driver IP Arama (LocalTel)")
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                val extras = Bundle().apply {
                    putBoolean(PhoneAccount.EXTRA_LOG_SELF_MANAGED_CALLS, false)
                }
                builder.setExtras(extras)
            }
            tm.registerPhoneAccount(builder.build())
        }
        return handle
    }

    fun reportIncomingCall(
        context: Context,
        callId: String,
        address: String,
        displayName: String,
    ): Boolean {
        val tm = context.getSystemService(TelecomManager::class.java) ?: return false
        val handle = ensurePhoneAccount(context) ?: return false
        return runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !tm.isIncomingCallPermitted(handle)) {
                return@runCatching false
            }
            val extras = Bundle().apply {
                putParcelable(TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE, handle)
                putParcelable(TelecomManager.EXTRA_INCOMING_CALL_ADDRESS, buildAddressUri(address))
                putString(EXTRA_CALL_ID, callId)
                putString(EXTRA_DISPLAY_NAME, displayName)
                putString(EXTRA_RAW_ADDRESS, address)
            }
            tm.addNewIncomingCall(handle, extras)
            true
        }.getOrDefault(false)
    }

    @SuppressLint("MissingPermission")
    fun reportOutgoingCall(
        context: Context,
        callId: String,
        address: String,
        displayName: String,
    ): Boolean {
        val tm = context.getSystemService(TelecomManager::class.java) ?: return false
        val handle = ensurePhoneAccount(context) ?: return false
        return runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !tm.isOutgoingCallPermitted(handle)) {
                return@runCatching false
            }
            val inner = Bundle().apply {
                putString(EXTRA_CALL_ID, callId)
                putString(EXTRA_DISPLAY_NAME, displayName)
                putString(EXTRA_RAW_ADDRESS, address)
            }
            val extras = Bundle().apply {
                putParcelable(TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE, handle)
                putBundle(TelecomManager.EXTRA_OUTGOING_CALL_EXTRAS, inner)
            }
            tm.placeCall(buildAddressUri(address), extras)
            true
        }.getOrDefault(false)
    }

    internal fun registerConnection(callId: String, connection: TrCallConnection) {
        if (callId.isNotBlank()) {
            connections[callId] = connection
        }
    }

    internal fun unregisterConnection(callId: String) {
        connections.remove(callId)
    }

    fun markActive(callId: String) {
        connections[callId]?.let { conn ->
            runCatching { conn.setActive() }
        }
    }

    fun markEnded(callId: String, reason: CallEndReason) {
        val conn = connections.remove(callId) ?: return
        val code = when (reason) {
            CallEndReason.LOCAL_HANGUP, CallEndReason.CANCELLED -> DisconnectCause.LOCAL
            CallEndReason.REMOTE_HANGUP -> DisconnectCause.REMOTE
            CallEndReason.REJECTED -> DisconnectCause.REJECTED
            CallEndReason.BUSY -> DisconnectCause.BUSY
            CallEndReason.NO_ANSWER -> DisconnectCause.MISSED
            else -> DisconnectCause.ERROR
        }
        runCatching {
            conn.setDisconnected(DisconnectCause(code))
            conn.destroy()
        }
    }

    fun buildAddressUri(rawAddress: String): Uri {
        val trimmed = rawAddress.trim()
        return when {
            trimmed.startsWith("sip:", ignoreCase = true) ||
                trimmed.startsWith("tel:", ignoreCase = true) -> Uri.parse(trimmed)
            trimmed.contains('@') -> Uri.fromParts(PhoneAccount.SCHEME_SIP, trimmed, null)
            else -> Uri.fromParts(PhoneAccount.SCHEME_TEL, trimmed, null)
        }
    }
}
