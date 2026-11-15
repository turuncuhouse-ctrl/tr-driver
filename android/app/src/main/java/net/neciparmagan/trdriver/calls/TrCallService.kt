package net.neciparmagan.trdriver.calls

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat

/**
 * Foreground service owning ongoing and incoming LocalTel calls.
 *
 * Uses `phoneCall` foreground service type while ringing and adds
 * `microphone` only once RECORD_AUDIO is granted so Android 14+ never rejects
 * an unanswered ringing notification.
 */
class TrCallService : Service() {

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val action = intent?.action.orEmpty()
        val callId = intent?.getStringExtra(EXTRA_CALL_ID).orEmpty()

        when (action) {
            ACTION_ACCEPT_CALL -> {
                CallController.acceptIncomingCall(applicationContext, callId)
            }
            ACTION_REJECT_CALL -> {
                CallController.rejectIncomingCall(applicationContext, callId)
                stopCallService()
                return START_NOT_STICKY
            }
            ACTION_HANGUP_CALL -> {
                CallController.hangupCall(applicationContext, callId)
                stopCallService()
                return START_NOT_STICKY
            }
            ACTION_STOP -> {
                stopCallService()
                return START_NOT_STICKY
            }
        }

        val state = CallController.currentState()
        if (!state.isLive) {
            stopCallService()
            return START_NOT_STICKY
        }

        val notification = CallNotifications.buildCallNotification(this, state)
        val needMic = state.phase == CallPhase.CONNECTING ||
            state.phase == CallPhase.ACTIVE ||
            state.phase == CallPhase.OUTGOING_DIALING ||
            state.phase == CallPhase.OUTGOING_RINGING
        startForegroundSafely(notification, needMic)
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        CallNotifications.cancelCallNotification(this)
        super.onDestroy()
    }

    private fun startForegroundSafely(notification: android.app.Notification, needMic: Boolean) {
        val hasMic = needMic && CallPermissions.hasMicrophone(this)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            val type = if (hasMic) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_PHONE_CALL or ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE
            } else {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_PHONE_CALL
            }
            try {
                ServiceCompat.startForeground(this, CallNotifications.CALL_NOTIFICATION_ID, notification, type)
            } catch (_: SecurityException) {
                runCatching {
                    ServiceCompat.startForeground(
                        this,
                        CallNotifications.CALL_NOTIFICATION_ID,
                        notification,
                        ServiceInfo.FOREGROUND_SERVICE_TYPE_PHONE_CALL,
                    )
                }
            }
        } else if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R && hasMic) {
            runCatching {
                ServiceCompat.startForeground(
                    this,
                    CallNotifications.CALL_NOTIFICATION_ID,
                    notification,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE,
                )
            }.onFailure {
                startForeground(CallNotifications.CALL_NOTIFICATION_ID, notification)
            }
        } else {
            startForeground(CallNotifications.CALL_NOTIFICATION_ID, notification)
        }
    }

    private fun stopCallService() {
        runCatching { stopForeground(STOP_FOREGROUND_REMOVE) }
        CallNotifications.cancelCallNotification(this)
        stopSelf()
    }

    companion object {
        const val ACTION_SYNC = "net.neciparmagan.trdriver.calls.SYNC"
        const val ACTION_ACCEPT_CALL = "net.neciparmagan.trdriver.calls.ACCEPT"
        const val ACTION_REJECT_CALL = "net.neciparmagan.trdriver.calls.REJECT"
        const val ACTION_HANGUP_CALL = "net.neciparmagan.trdriver.calls.HANGUP"
        const val ACTION_STOP = "net.neciparmagan.trdriver.calls.STOP"
        const val EXTRA_CALL_ID = "call_id"

        fun sync(context: Context) {
            val state = CallController.currentState()
            if (!state.isLive) {
                stop(context)
                return
            }
            val intent = Intent(context, TrCallService::class.java).apply {
                action = ACTION_SYNC
                putExtra(EXTRA_CALL_ID, state.callId)
            }
            runCatching { ContextCompat.startForegroundService(context, intent) }
        }

        fun stop(context: Context) {
            val intent = Intent(context, TrCallService::class.java).apply {
                action = ACTION_STOP
            }
            runCatching { context.startService(intent) }
            CallNotifications.cancelCallNotification(context)
        }
    }
}
