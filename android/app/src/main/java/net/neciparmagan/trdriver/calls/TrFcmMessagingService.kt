package net.neciparmagan.trdriver.calls

import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * Receives optional high-priority data pushes from the server
 * (`FIREBASE_SERVICE_ACCOUNT` configured). A push's only job is to wake the
 * process: the real invite always arrives over the gateway WebSocket, which
 * the server re-sends every few seconds until the call is answered or times
 * out. Builds without a google-services.json never receive pushes; the class
 * is then simply never instantiated.
 *
 * No payload content is logged (call ids and numbers are personal data).
 */
class TrFcmMessagingService : FirebaseMessagingService() {

    override fun onNewToken(token: String) {
        FcmRegistrar.uploadNewToken(applicationContext, token)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        if (message.data["type"] != "localtel_invite") return
        // Wake the signaling stack; the retried invite rings the phone.
        LocalTelListenerService.syncState(applicationContext)
        CallController.ensureProviderStarted(applicationContext)
    }
}
