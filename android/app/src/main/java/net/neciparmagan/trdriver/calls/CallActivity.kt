package net.neciparmagan.trdriver.calls

import android.Manifest
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.GridLayout
import android.widget.ImageButton
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.R

/**
 * Dedicated full-screen / lock-screen call screen.
 *
 * Shows fixed, non-shifting large Aç (Accept) / Reddet (Decline) buttons when
 * ringing on the lock screen and switches cleanly to call controls once
 * answered.
 */
class CallActivity : AppCompatActivity() {

    private lateinit var tvCallerInitials: TextView
    private lateinit var tvCallerName: TextView
    private lateinit var tvCallerNumber: TextView
    private lateinit var tvCallStatus: TextView
    private lateinit var incomingActionsRow: View
    private lateinit var activeActionsContainer: View
    private lateinit var keypadOverlay: View
    private lateinit var tvDtmfBuffer: TextView
    private lateinit var btnMute: ImageButton
    private lateinit var btnSpeaker: ImageButton
    private lateinit var btnKeypad: ImageButton

    private var keypadVisible = false
    private var askedMicThisCall = false
    private var currentCallId = ""
    private var finishScheduled = false

    private val tickerHandler = Handler(Looper.getMainLooper())
    private val tickerRunnable = object : Runnable {
        override fun run() {
            val state = CallController.currentState()
            if (state.phase == CallPhase.ACTIVE && state.connectedAtMs > 0L) {
                tvCallStatus.text = CallStateMachine.formatDuration(state.elapsedMs(System.currentTimeMillis()))
                tickerHandler.postDelayed(this, 500L)
            }
        }
    }

    private val micPermissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) {
                TrCallService.sync(this)
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableLockScreenBehavior()
        setContentView(R.layout.activity_call)

        val root = findViewById<View>(R.id.callRoot)
        val baseLeft = root.paddingLeft
        val baseRight = root.paddingRight
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            v.setPadding(baseLeft, bars.top, baseRight, bars.bottom)
            insets
        }

        tvCallerInitials = findViewById(R.id.tvCallerInitials)
        tvCallerName = findViewById(R.id.tvCallerName)
        tvCallerNumber = findViewById(R.id.tvCallerNumber)
        tvCallStatus = findViewById(R.id.tvCallStatus)
        incomingActionsRow = findViewById(R.id.incomingActionsRow)
        activeActionsContainer = findViewById(R.id.activeActionsContainer)
        keypadOverlay = findViewById(R.id.keypadOverlay)
        tvDtmfBuffer = findViewById(R.id.tvDtmfBuffer)
        btnMute = findViewById(R.id.btnMute)
        btnSpeaker = findViewById(R.id.btnSpeaker)
        btnKeypad = findViewById(R.id.btnKeypad)

        buildDtmfGrid()

        findViewById<ImageButton>(R.id.btnAccept).setOnClickListener {
            val id = currentCallId.ifBlank { CallController.currentState().callId }
            if (!CallPermissions.hasMicrophone(this) && !askedMicThisCall) {
                askedMicThisCall = true
                CallController.acceptIncomingCall(this, id)
                CallPermissions.showMicrophoneExplanation(this) {
                    micPermissionLauncher.launch(Manifest.permission.RECORD_AUDIO)
                }
            } else {
                CallController.acceptIncomingCall(this, id)
            }
        }

        findViewById<ImageButton>(R.id.btnDecline).setOnClickListener {
            val id = currentCallId.ifBlank { CallController.currentState().callId }
            CallController.rejectIncomingCall(this, id)
        }

        findViewById<ImageButton>(R.id.btnHangup).setOnClickListener {
            val id = currentCallId.ifBlank { CallController.currentState().callId }
            CallController.hangupCall(this, id)
        }

        btnMute.setOnClickListener { CallController.toggleMute(this) }
        btnSpeaker.setOnClickListener { CallController.toggleSpeaker(this) }
        btnKeypad.setOnClickListener {
            keypadVisible = !keypadVisible
            keypadOverlay.visibility = if (keypadVisible) View.VISIBLE else View.GONE
            btnKeypad.setBackgroundResource(
                if (keypadVisible) R.drawable.bg_call_control_active else R.drawable.bg_call_control_circle,
            )
        }

        onBackPressedDispatcher.addCallback(
            this,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    if (keypadVisible) {
                        keypadVisible = false
                        keypadOverlay.visibility = View.GONE
                        btnKeypad.setBackgroundResource(R.drawable.bg_call_control_circle)
                    }
                    // Active calls stay pinned unless explicitly minimized via home button.
                }
            },
        )

        observeCallState()
    }

    override fun onDestroy() {
        tickerHandler.removeCallbacks(tickerRunnable)
        super.onDestroy()
    }

    private fun observeCallState() {
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                CallController.callState.collect { state ->
                    renderState(state)
                }
            }
        }
    }

    private fun renderState(state: CallSessionState) {
        if (state.callId.isNotBlank()) {
            currentCallId = state.callId
        }
        val displayName = state.remoteDisplayName.ifBlank {
            state.remoteAddress.ifBlank { "LocalTel Araması" }
        }
        tvCallerName.text = displayName
        tvCallerInitials.text = initialsFor(displayName)

        val formattedNum = PhoneNumberNormalizer.formatForDisplay(state.remoteAddress)
        tvCallerNumber.text = buildString {
            if (formattedNum.isNotBlank() && formattedNum != displayName) {
                append(formattedNum)
            }
            if (state.contactLabel.isNotBlank()) {
                if (isNotEmpty()) append(" · ")
                append(state.contactLabel)
            }
        }

        tickerHandler.removeCallbacks(tickerRunnable)
        when (state.phase) {
            CallPhase.INCOMING_RINGING -> {
                tvCallStatus.text = "Gelen LocalTel araması..."
                incomingActionsRow.visibility = View.VISIBLE
                activeActionsContainer.visibility = View.GONE
                keypadOverlay.visibility = View.GONE
                window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            }
            CallPhase.OUTGOING_DIALING -> {
                tvCallStatus.text = "Aranıyor..."
                incomingActionsRow.visibility = View.GONE
                activeActionsContainer.visibility = View.VISIBLE
                window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                maybePromptMicDuringCall()
            }
            CallPhase.OUTGOING_RINGING -> {
                tvCallStatus.text = "Çalıyor..."
                incomingActionsRow.visibility = View.GONE
                activeActionsContainer.visibility = View.VISIBLE
                window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            }
            CallPhase.CONNECTING -> {
                tvCallStatus.text = "Bağlanıyor..."
                incomingActionsRow.visibility = View.GONE
                activeActionsContainer.visibility = View.VISIBLE
                window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                maybePromptMicDuringCall()
            }
            CallPhase.ACTIVE -> {
                incomingActionsRow.visibility = View.GONE
                activeActionsContainer.visibility = View.VISIBLE
                window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                tickerHandler.post(tickerRunnable)
            }
            CallPhase.ENDED -> {
                window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                incomingActionsRow.visibility = View.GONE
                activeActionsContainer.visibility = View.GONE
                keypadOverlay.visibility = View.GONE
                tvCallStatus.text = endReasonText(state)
                scheduleFinish()
            }
            CallPhase.IDLE -> {
                window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                if (currentCallId.isBlank() || !finishScheduled) {
                    finishAndRemoveTaskSafely()
                }
            }
            CallPhase.ENDING -> {
                tvCallStatus.text = "Sonlandırılıyor..."
            }
        }

        btnMute.setImageResource(if (state.muted) R.drawable.ic_mic_off else R.drawable.ic_mic)
        btnMute.setBackgroundResource(
            if (state.muted) R.drawable.bg_call_control_active else R.drawable.bg_call_control_circle,
        )
        btnSpeaker.setBackgroundResource(
            if (state.speakerOn) R.drawable.bg_call_control_active else R.drawable.bg_call_control_circle,
        )
    }

    private fun maybePromptMicDuringCall() {
        if (!askedMicThisCall && !CallPermissions.hasMicrophone(this)) {
            askedMicThisCall = true
            CallPermissions.showMicrophoneExplanation(this) {
                micPermissionLauncher.launch(Manifest.permission.RECORD_AUDIO)
            }
        }
    }

    private fun scheduleFinish() {
        if (finishScheduled) return
        finishScheduled = true
        tickerHandler.postDelayed({ finishAndRemoveTaskSafely() }, 950L)
    }

    private fun finishAndRemoveTaskSafely() {
        if (isFinishing || isDestroyed) return
        runCatching { finishAndRemoveTask() }.onFailure { finish() }
    }

    @Suppress("DEPRECATION")
    private fun enableLockScreenBehavior() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O_MR1) {
            setShowWhenLocked(true)
            setTurnScreenOn(true)
        } else {
            window.addFlags(
                WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED or
                    WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON,
            )
        }
    }

    private fun buildDtmfGrid() {
        val grid = findViewById<GridLayout>(R.id.dtmfGrid)
        val keys = listOf('1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#')
        val density = resources.displayMetrics.density
        val sizePx = (64 * density).toInt()
        val marginPx = (6 * density).toInt()

        for (k in keys) {
            val btn = Button(this).apply {
                text = k.toString()
                textSize = 20f
                setTextColor(0xFFFFFFFF.toInt())
                gravity = Gravity.CENTER
                setBackgroundResource(R.drawable.bg_call_control_circle)
                layoutParams = GridLayout.LayoutParams().apply {
                    width = sizePx
                    height = sizePx
                    setMargins(marginPx, marginPx, marginPx, marginPx)
                }
                setOnClickListener {
                    CallController.sendDtmf(k)
                    val prev = tvDtmfBuffer.text?.toString().orEmpty()
                    tvDtmfBuffer.text = (prev + k).takeLast(18)
                }
            }
            grid.addView(btn)
        }
    }

    private fun endReasonText(state: CallSessionState): String {
        if (state.errorMessage.isNotBlank()) return state.errorMessage
        return when (state.endReason) {
            CallEndReason.REJECTED -> "Arama reddedildi"
            CallEndReason.CANCELLED -> "Arama iptal edildi"
            CallEndReason.BUSY -> "Meşgul: karşı taraf başka bir görüşmede"
            CallEndReason.NOT_FOUND -> "Bulunamadı: bu numara kayıtlı bir TR Driver kullanıcısına ait değil"
            CallEndReason.OFFLINE -> "Çevrimdışı: karşı tarafın uygulaması şu an bağlı değil"
            CallEndReason.NO_ANSWER -> "Cevap verilmedi"
            CallEndReason.REMOTE_HANGUP -> "Karşı taraf kapattı"
            CallEndReason.NETWORK_ERROR -> "Bağlantı hatası"
            CallEndReason.CONFIG_ERROR -> "LocalTel yapılandırma hatası"
            else -> "Arama sonlandırıldı"
        }
    }

    private fun initialsFor(name: String): String {
        val parts = name.trim().split(Regex("\\s+")).filter { it.isNotEmpty() }
        if (parts.isEmpty()) return "LT"
        if (parts.size == 1) {
            val word = parts[0]
            return word.take(2).uppercase()
        }
        return "${parts.first().first().uppercaseChar()}${parts.last().first().uppercaseChar()}"
    }

    companion object {
        const val EXTRA_CALL_ID = "extra_call_id"
    }
}
