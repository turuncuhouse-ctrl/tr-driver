package net.neciparmagan.trdriver.calls

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.Person
import net.neciparmagan.trdriver.R

object CallNotifications {
    const val CHANNEL_INCOMING = "trdriver_calls_incoming"
    const val CHANNEL_ONGOING = "trdriver_calls_ongoing"
    const val CHANNEL_MISSED = "trdriver_calls_missed"
    const val CHANNEL_LISTENER = "trdriver_localtel_listener"

    const val CALL_NOTIFICATION_ID = 70
    const val LISTENER_NOTIFICATION_ID = 71
    private const val MISSED_NOTIFICATION_BASE = 80

    fun ensureChannels(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = context.getSystemService(NotificationManager::class.java) ?: return

        val incoming = NotificationChannel(
            CHANNEL_INCOMING,
            "Gelen LocalTel Aramaları",
            NotificationManager.IMPORTANCE_HIGH,
        ).apply {
            description = "Kilit ekranı ve tam ekran gelen sesli arama bildirimleri"
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
            setSound(null, null) // CallAudioController owns ringtone lifecycle.
            enableVibration(false)
            setShowBadge(false)
        }

        val ongoing = NotificationChannel(
            CHANNEL_ONGOING,
            "Devam Eden Aramalar",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Aktif LocalTel görüşmesi"
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
            setShowBadge(false)
        }

        val missed = NotificationChannel(
            CHANNEL_MISSED,
            "Cevapsız Aramalar",
            NotificationManager.IMPORTANCE_DEFAULT,
        ).apply {
            description = "Cevapsız LocalTel araması bildirimleri"
            lockscreenVisibility = Notification.VISIBILITY_PRIVATE
        }

        val listener = NotificationChannel(
            CHANNEL_LISTENER,
            "LocalTel Arama Hattı",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Gelen IP aramaları için arka plan bağlantı durumu"
            setShowBadge(false)
        }

        nm.createNotificationChannel(incoming)
        nm.createNotificationChannel(ongoing)
        nm.createNotificationChannel(missed)
        nm.createNotificationChannel(listener)
    }

    fun buildCallNotification(context: Context, state: CallSessionState): Notification {
        ensureChannels(context)

        val callerName = state.remoteDisplayName.ifBlank {
            state.remoteAddress.ifBlank { "LocalTel Araması" }
        }
        val person = Person.Builder()
            .setName(callerName)
            .setImportant(true)
            .build()

        val fullScreenIntent = Intent(context, CallActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or
                Intent.FLAG_ACTIVITY_SINGLE_TOP or
                Intent.FLAG_ACTIVITY_CLEAR_TOP or
                Intent.FLAG_ACTIVITY_NO_USER_ACTION
            putExtra(CallActivity.EXTRA_CALL_ID, state.callId)
        }
        val contentPi = PendingIntent.getActivity(
            context,
            1001,
            fullScreenIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )

        if (state.phase == CallPhase.INCOMING_RINGING) {
            val answerPi = serviceActionPi(context, TrCallService.ACTION_ACCEPT_CALL, state.callId, 1002)
            val declinePi = serviceActionPi(context, TrCallService.ACTION_REJECT_CALL, state.callId, 1003)

            val style = NotificationCompat.CallStyle.forIncomingCall(person, declinePi, answerPi)
                .setAnswerButtonColorHint(Color.parseColor("#16A34A"))
                .setDeclineButtonColorHint(Color.parseColor("#DC2626"))

            val sub = buildString {
                append("LocalTel Sesli Arama")
                if (state.matchedFromContacts && state.remoteAddress.isNotBlank() && state.remoteAddress != callerName) {
                    append(" · ").append(PhoneNumberNormalizer.formatForDisplay(state.remoteAddress))
                }
            }

            return NotificationCompat.Builder(context, CHANNEL_INCOMING)
                .setSmallIcon(R.drawable.ic_stat_call)
                .setContentTitle(callerName)
                .setContentText(sub)
                .setStyle(style)
                .setCategory(NotificationCompat.CATEGORY_CALL)
                .setPriority(NotificationCompat.PRIORITY_MAX)
                .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
                .setOngoing(true)
                .setAutoCancel(false)
                .setOnlyAlertOnce(true)
                .setContentIntent(contentPi)
                .setFullScreenIntent(contentPi, true)
                .build()
        }

        val hangupPi = serviceActionPi(context, TrCallService.ACTION_HANGUP_CALL, state.callId, 1004)
        val style = NotificationCompat.CallStyle.forOngoingCall(person, hangupPi)
            .setDeclineButtonColorHint(Color.parseColor("#DC2626"))

        val phaseText = when (state.phase) {
            CallPhase.OUTGOING_DIALING -> "Aranıyor..."
            CallPhase.OUTGOING_RINGING -> "Çalıyor..."
            CallPhase.CONNECTING -> "Bağlanıyor..."
            CallPhase.ACTIVE -> if (state.muted) "Mikrofon kapalı · Görüşme sürüyor" else "Görüşme sürüyor"
            else -> "LocalTel araması"
        }

        val builder = NotificationCompat.Builder(context, CHANNEL_ONGOING)
            .setSmallIcon(R.drawable.ic_stat_call)
            .setContentTitle(callerName)
            .setContentText(phaseText)
            .setStyle(style)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(contentPi)

        if (state.phase == CallPhase.ACTIVE && state.connectedAtMs > 0L) {
            builder.setUsesChronometer(true)
            builder.setWhen(state.connectedAtMs)
            builder.setShowWhen(true)
        }
        return builder.build()
    }

    fun buildListenerNotification(context: Context, status: ProviderStatusSnapshot): Notification {
        ensureChannels(context)
        val openCalls = PendingIntent.getActivity(
            context,
            1010,
            Intent(context, CallsActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(context, CHANNEL_LISTENER)
            .setSmallIcon(R.drawable.ic_stat_call)
            .setContentTitle("LocalTel hattı")
            .setContentText(status.statusText.ifBlank { "Hazır" })
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setContentIntent(openCalls)
            .build()
    }

    fun notifyMissedCall(context: Context, state: CallSessionState) {
        ensureChannels(context)
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        val callerName = state.remoteDisplayName.ifBlank {
            state.remoteAddress.ifBlank { "Bilinmeyen numara" }
        }
        val publicPlaceholder = NotificationCompat.Builder(context, CHANNEL_MISSED)
            .setSmallIcon(R.drawable.ic_stat_call)
            .setContentTitle("Cevapsız LocalTel araması")
            .setContentText("Ayrıntıları görmek için kilidi açın")
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .build()

        val openCalls = PendingIntent.getActivity(
            context,
            1011,
            Intent(context, CallsActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )

        val notif = NotificationCompat.Builder(context, CHANNEL_MISSED)
            .setSmallIcon(R.drawable.ic_stat_call)
            .setContentTitle("Cevapsız arama")
            .setContentText(callerName)
            .setAutoCancel(true)
            .setContentIntent(openCalls)
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setPublicVersion(publicPlaceholder)
            .build()

        val id = MISSED_NOTIFICATION_BASE + (state.callId.hashCode() and 0x0f)
        runCatching { nm.notify(id, notif) }
    }

    fun cancelCallNotification(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        runCatching { nm.cancel(CALL_NOTIFICATION_ID) }
    }

    private fun serviceActionPi(
        context: Context,
        action: String,
        callId: String,
        requestCode: Int,
    ): PendingIntent {
        val intent = Intent(context, TrCallService::class.java).apply {
            this.action = action
            putExtra(TrCallService.EXTRA_CALL_ID, callId)
        }
        return PendingIntent.getService(
            context,
            requestCode,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }
}
