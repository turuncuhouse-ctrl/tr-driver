package net.neciparmagan.trdriver.calls

import android.os.Build
import android.telecom.CallAudioState
import android.telecom.Connection
import android.telecom.ConnectionRequest
import android.telecom.ConnectionService
import android.telecom.DisconnectCause
import android.telecom.PhoneAccountHandle
import android.telecom.TelecomManager

class TrConnectionService : ConnectionService() {

    override fun onCreateIncomingConnection(
        connectionManagerPhoneAccount: PhoneAccountHandle?,
        request: ConnectionRequest?,
    ): Connection {
        val extras = request?.extras
        val callId = extras?.getString(TelecomHelper.EXTRA_CALL_ID).orEmpty()
        val displayName = extras?.getString(TelecomHelper.EXTRA_DISPLAY_NAME).orEmpty()
        val rawAddr = extras?.getString(TelecomHelper.EXTRA_RAW_ADDRESS)
            ?: request?.address?.schemeSpecificPart.orEmpty()

        val conn = TrCallConnection(applicationContext, callId).apply {
            setConnectionProperties(Connection.PROPERTY_SELF_MANAGED)
            setAudioModeIsVoip(true)
            setAddress(TelecomHelper.buildAddressUri(rawAddr), TelecomManager.PRESENTATION_ALLOWED)
            if (displayName.isNotBlank()) {
                setCallerDisplayName(displayName, TelecomManager.PRESENTATION_ALLOWED)
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N_MR1) {
                setConnectionCapabilities(Connection.CAPABILITY_MUTE or Connection.CAPABILITY_HOLD)
            }
            setRinging()
        }
        TelecomHelper.registerConnection(callId, conn)
        return conn
    }

    override fun onCreateIncomingConnectionFailed(
        connectionManagerPhoneAccount: PhoneAccountHandle?,
        request: ConnectionRequest?,
    ) {
        val callId = request?.extras?.getString(TelecomHelper.EXTRA_CALL_ID).orEmpty()
        if (callId.isNotBlank()) {
            CallController.onTelecomShowIncomingUi(applicationContext, callId)
        }
    }

    override fun onCreateOutgoingConnection(
        connectionManagerPhoneAccount: PhoneAccountHandle?,
        request: ConnectionRequest?,
    ): Connection {
        val extras = request?.extras
        val callId = extras?.getString(TelecomHelper.EXTRA_CALL_ID).orEmpty()
        val displayName = extras?.getString(TelecomHelper.EXTRA_DISPLAY_NAME).orEmpty()
        val rawAddr = extras?.getString(TelecomHelper.EXTRA_RAW_ADDRESS)
            ?: request?.address?.schemeSpecificPart.orEmpty()

        val conn = TrCallConnection(applicationContext, callId).apply {
            setConnectionProperties(Connection.PROPERTY_SELF_MANAGED)
            setAudioModeIsVoip(true)
            setAddress(TelecomHelper.buildAddressUri(rawAddr), TelecomManager.PRESENTATION_ALLOWED)
            if (displayName.isNotBlank()) {
                setCallerDisplayName(displayName, TelecomManager.PRESENTATION_ALLOWED)
            }
            setConnectionCapabilities(Connection.CAPABILITY_MUTE)
            setDialing()
        }
        TelecomHelper.registerConnection(callId, conn)
        return conn
    }

    override fun onCreateOutgoingConnectionFailed(
        connectionManagerPhoneAccount: PhoneAccountHandle?,
        request: ConnectionRequest?,
    ) {
        // CallController drives the call UI directly if Telecom rejects self-managed creation.
    }
}

internal class TrCallConnection(
    private val appContext: android.content.Context,
    private val callId: String,
) : Connection() {

    override fun onShowIncomingCallUi() {
        CallController.onTelecomShowIncomingUi(appContext, callId)
    }

    override fun onAnswer() {
        CallController.acceptIncomingCall(appContext, callId)
    }

    override fun onAnswer(videoState: Int) {
        CallController.acceptIncomingCall(appContext, callId)
    }

    override fun onReject() {
        CallController.rejectIncomingCall(appContext, callId)
    }

    override fun onReject(rejectReason: Int) {
        CallController.rejectIncomingCall(appContext, callId)
    }

    override fun onReject(replyMessage: String?) {
        CallController.rejectIncomingCall(appContext, callId)
    }

    override fun onDisconnect() {
        CallController.hangupCall(appContext, callId)
    }

    override fun onAbort() {
        CallController.hangupCall(appContext, callId)
    }

    override fun onHold() {
        CallController.setHold(appContext, callId, true)
        setOnHold()
    }

    override fun onUnhold() {
        CallController.setHold(appContext, callId, false)
        setActive()
    }

    @Suppress("DEPRECATION")
    override fun onCallAudioStateChanged(state: CallAudioState?) {
        if (state == null) return
        CallController.syncSystemMute(callId, state.isMuted)
    }

    override fun onStateChanged(state: Int) {
        if (state == STATE_DISCONNECTED) {
            TelecomHelper.unregisterConnection(callId)
        }
    }
}
