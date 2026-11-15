package net.neciparmagan.trdriver.calls

import android.annotation.SuppressLint
import android.content.pm.PackageManager
import android.telephony.TelephonyManager
import android.text.InputType
import android.widget.EditText
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.SessionStore

/**
 * Mandatory one-step prompt: every member registers their own phone number so
 * other members' address books match them (WhatsApp style). Pre-filled from the
 * SIM when Android allows it.
 */
object MyPhonePrompt {
    @Volatile
    private var showing = false

    fun ensure(activity: AppCompatActivity, session: SessionStore) {
        if (!session.isLoggedIn || session.myPhoneRegistered || showing) return
        activity.lifecycleScope.launch {
            val api = LocalTelConfigApi(session)
            val current = api.getMyPhone().getOrNull() ?: return@launch
            if (current.first.isNotBlank()) {
                session.myPhoneRegistered = true
                return@launch
            }
            if (showing || activity.isFinishing || activity.isDestroyed) return@launch
            show(activity, session, api, deviceNumber(activity))
        }
    }

    @SuppressLint("MissingPermission", "HardwareIds")
    private fun deviceNumber(activity: AppCompatActivity): String {
        val ok = listOf(
            android.Manifest.permission.READ_PHONE_NUMBERS,
            android.Manifest.permission.READ_PHONE_STATE,
        ).any { activity.checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED }
        if (!ok) return ""
        return runCatching {
            @Suppress("DEPRECATION")
            activity.getSystemService(TelephonyManager::class.java)?.line1Number.orEmpty()
        }.getOrDefault("")
    }

    private fun show(activity: AppCompatActivity, session: SessionStore, api: LocalTelConfigApi, suggestion: String) {
        showing = true
        val input = EditText(activity).apply {
            hint = "05xx xxx xx xx"
            inputType = InputType.TYPE_CLASS_PHONE
            setText(suggestion)
            setPadding(48, 32, 48, 16)
        }
        val dialog = AlertDialog.Builder(activity)
            .setTitle("Telefon numaranız")
            .setMessage(
                "Arkadaşlarınızın rehberinde sizi TR Driver kullanıcısı olarak görebilmesi ve " +
                    "sizi numaranızla arayabilmesi için kendi numaranızı bir kez girin.",
            )
            .setView(input)
            .setCancelable(false)
            .setPositiveButton("Kaydet", null)
            .create()
        dialog.setOnDismissListener { showing = false }
        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val typed = input.text?.toString().orEmpty()
                activity.lifecycleScope.launch {
                    api.setMyPhone(typed).onSuccess { (phone, _) ->
                        if (phone.isBlank()) {
                            Toast.makeText(activity, "Geçerli bir numara girin", Toast.LENGTH_LONG).show()
                        } else {
                            session.myPhoneRegistered = true
                            Toast.makeText(activity, "Numaranız kaydedildi", Toast.LENGTH_LONG).show()
                            dialog.dismiss()
                        }
                    }.onFailure {
                        Toast.makeText(activity, it.message ?: "Numara kaydedilemedi", Toast.LENGTH_LONG).show()
                    }
                }
            }
        }
        dialog.show()
    }
}
