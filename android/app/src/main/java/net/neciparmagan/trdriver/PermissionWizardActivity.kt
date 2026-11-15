package net.neciparmagan.trdriver

import android.Manifest
import android.app.Activity
import android.app.NotificationManager
import android.app.role.RoleManager
import android.content.ContentValues
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.provider.Telephony
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.decodeFromJsonElement
import net.neciparmagan.trdriver.data.PersonalBackupApi
import net.neciparmagan.trdriver.data.PersonalBackupPolicy
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.SmsBackupRecord
import net.neciparmagan.trdriver.calls.CallPermissions
import net.neciparmagan.trdriver.backup.TrKeepAliveService

/** Turkish, one-permission-at-a-time walkthrough for runtime and special access. */
class PermissionWizardActivity : AppCompatActivity() {
    private enum class Special { NONE, ALL_FILES, BATTERY, FULL_SCREEN, UNKNOWN_SOURCES }
    private data class Step(
        val title: String,
        val reason: String,
        val permissions: List<String> = emptyList(),
        val special: Special = Special.NONE,
        val key: String,
    )

    private lateinit var title: TextView
    private lateinit var progress: TextView
    private lateinit var reason: TextView
    private lateinit var status: TextView
    private lateinit var illustration: TextView
    private lateinit var action: Button
    private lateinit var previous: Button
    private lateinit var next: Button
    private lateinit var restoreSms: Button
    private lateinit var steps: List<Step>
    private var index = 0
    private var previousSmsPackage: String? = null
    private var restoreAfterRoleRequest = false
    private var returnToPreviousSmsAppAfterSettings = false
    private val session by lazy { SessionStore(this) }
    private val askedPrefs by lazy { getSharedPreferences(PREFS, MODE_PRIVATE) }

    private val permissionRequest = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        if (steps.getOrNull(index)?.key == "sms") askedPrefs.edit().putBoolean(KEY_SMS_ASKED, true).apply()
        render()
    }

    private val smsRoleRequest = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == Activity.RESULT_OK && restoreAfterRoleRequest) {
            restoreAfterRoleRequest = false
            restoreSmsBackup()
        } else {
            restoreAfterRoleRequest = false
            Toast.makeText(this, "SMS geri yükleme için varsayılan SMS rolü gerekli", Toast.LENGTH_LONG).show()
        }
        render()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        index = intent.getIntExtra(EXTRA_STEP, 0)
        previousSmsPackage = askedPrefs.getString(KEY_PREVIOUS_SMS_PACKAGE, null)
        steps = buildSteps()
        buildLayout()
        render()
    }

    override fun onResume() {
        super.onResume()
        if (returnToPreviousSmsAppAfterSettings) {
            val currentSmsApp = Telephony.Sms.getDefaultSmsPackage(this)
            if (!currentSmsApp.isNullOrBlank() && currentSmsApp != packageName) {
                returnToPreviousSmsAppAfterSettings = false
                if (currentSmsApp == previousSmsPackage) {
                    askedPrefs.edit().remove(KEY_PREVIOUS_SMS_PACKAGE).apply()
                    runCatching { packageManager.getLaunchIntentForPackage(currentSmsApp)?.let(::startActivity) }
                }
            }
        }
        if (::steps.isInitialized) render()
    }

    private fun buildSteps(): List<Step> {
        val mediaPermissions = when {
            Build.VERSION.SDK_INT >= 34 -> listOf(
                Manifest.permission.READ_MEDIA_IMAGES,
                Manifest.permission.READ_MEDIA_VIDEO,
                Manifest.permission.READ_MEDIA_AUDIO,
                Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED,
            )
            Build.VERSION.SDK_INT >= 33 -> listOf(
                Manifest.permission.READ_MEDIA_IMAGES,
                Manifest.permission.READ_MEDIA_VIDEO,
                Manifest.permission.READ_MEDIA_AUDIO,
            )
            else -> listOf(Manifest.permission.READ_EXTERNAL_STORAGE)
        }
        return listOf(
            Step("Rehber", "Kişi adlarını göstermek ve rehberden arama başlatmak için gereklidir. Rehber sunucuya yüklenmez.", listOf(Manifest.permission.READ_CONTACTS), key = "contacts"),
            Step("SMS okuma", "SMS yedeklemesini okuyabilmek için gereklidir. İzin yoksa SMS yedekleme duraklatılır.", listOf(Manifest.permission.READ_SMS), key = "sms"),
            Step("Telefon ve arama", "Telefon durumunu denetlemek, ConnectionService ile gelen aramaları göstermek için gereklidir.", listOf(Manifest.permission.READ_PHONE_STATE), key = "phone"),
            Step("Mikrofon", "LocalTel görüşmesinde sesinizi canlı iletmek için gereklidir; ses kaydı yapılmaz.", listOf(Manifest.permission.RECORD_AUDIO), key = "microphone"),
            Step("Bildirimler", "Yedekleme durumunu ve gelen aramaları bildirmek için gereklidir.", if (Build.VERSION.SDK_INT >= 33) listOf(Manifest.permission.POST_NOTIFICATIONS) else emptyList(), key = "notifications"),
            Step("Fotoğraf, video ve ses", "Seçtiğiniz medya dosyalarını yedeklemek ve görüntülemek için gereklidir.", mediaPermissions, key = "media"),
            Step("Kısmi medya erişimi", "Android 14 ve üzeri sürümlerde yalnızca seçtiğiniz fotoğraf ve videolara erişim seçeneğidir.", if (Build.VERSION.SDK_INT >= 34) listOf(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED) else emptyList(), key = "partial-media"),
            Step("Tüm dosyalara erişim", "İsteğe bağlı klasör yedeklemesi için gerekir. Verilmezse klasör seçici kullanılabilir.", special = Special.ALL_FILES, key = "all-files"),
            Step("Pil optimizasyonu", "Arka plan yedeklemesinin ve gelen çağrı dinlemesinin uyku sırasında durmaması için önerilir.", special = Special.BATTERY, key = "battery"),
            Step("Tam ekran arama bildirimi", "Telefon kilitliyken gelen aramada Aç / Reddet ekranını göstermek için gereklidir.", special = Special.FULL_SCREEN, key = "full-screen"),
            Step("Bilinmeyen kaynaktan kurulum", "TR Driver içinden indirilen uygulama güncellemelerini kurabilmek için gereklidir.", special = Special.UNKNOWN_SOURCES, key = "unknown-sources"),
            Step("Kamera", "TR Driver içindeki araç kabul ve belge fotoğrafı çekme özellikleri için gereklidir.", listOf(Manifest.permission.CAMERA), key = "camera"),
            Step("Bluetooth", "Bluetooth kulaklık ve araç ses sistemini aramalarda kullanmak için gereklidir.", if (Build.VERSION.SDK_INT >= 31) listOf(Manifest.permission.BLUETOOTH_CONNECT) else emptyList(), key = "bluetooth"),
        )
    }

    private fun buildLayout() {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(22), dp(20), dp(22), dp(20))
            setBackgroundColor(0xFFF4F8FC.toInt())
        }
        title = textView(25, true).also { root.addView(it) }
        progress = textView(14, false).apply { setPadding(0, dp(6), 0, dp(14)); root.addView(this) }
        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(18), dp(18), dp(18), dp(18))
            setBackgroundColor(0xFFFFFFFF.toInt())
        }
        reason = textView(16, false).also { card.addView(it) }
        status = textView(15, true).apply { setPadding(0, dp(16), 0, dp(4)); card.addView(this) }
        illustration = textView(14, false).apply {
            setPadding(dp(12), dp(10), dp(12), dp(10))
            setBackgroundColor(0xFFEAF2FB.toInt())
            card.addView(this)
        }
        action = Button(this).apply { isAllCaps = false; card.addView(this) }
        restoreSms = Button(this).apply { text = "SMS yedeğini geri yükle"; isAllCaps = false; visibility = View.GONE; card.addView(this) }
        root.addView(card, LinearLayout.LayoutParams(-1, 0, 1f))
        val nav = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL }
        previous = Button(this).apply { text = "Geri"; isAllCaps = false; nav.addView(this, LinearLayout.LayoutParams(0, -2, 1f)) }
        next = Button(this).apply { isAllCaps = false; nav.addView(this, LinearLayout.LayoutParams(0, -2, 1f)) }
        root.addView(nav)
        setContentView(ScrollView(this).apply { addView(root) })

        action.setOnClickListener { actOnCurrentStep() }
        previous.setOnClickListener { if (index > 0) { index--; render() } }
        next.setOnClickListener { advance() }
        restoreSms.setOnClickListener { requestSmsRestoreRole() }
    }

    private fun render() {
        if (!::steps.isInitialized || steps.isEmpty()) return
        index = index.coerceIn(0, steps.lastIndex)
        val step = steps[index]
        title.text = step.title
        progress.text = "Adım ${index + 1} / ${steps.size}"
        reason.text = step.reason
        val state = statusOf(step)
        status.text = "Durum: $state"
        status.setTextColor(if (state.startsWith("Verildi")) 0xFF1B6E3B.toInt() else 0xFF9A4D00.toInt())
        val restricted = step.key == "sms" && isRestrictedSmsPermission()
        illustration.visibility = if (restricted) View.VISIBLE else View.GONE
        if (restricted) {
            illustration.text = "Android güvenlik adımı\n\n① Uygulama bilgisi ekranını açın.\n② Sağ üstte ⋮ menüsüne dokunun.\n③ “Kısıtlı ayarlara izin ver” seçeneğini açın.\n④ Buraya dönüp SMS iznini yeniden verin."
        }
        action.text = when {
            state.startsWith("Verildi") -> "İzin durumunu yenile"
            isPermanentDenial(step) -> "Uygulama ayarlarını aç"
            step.special != Special.NONE -> "İlgili ayarı aç"
            else -> "İzin iste"
        }
        previous.isEnabled = index > 0
        next.text = if (index == steps.lastIndex) "Tamamla" else "Sonraki"
        restoreSms.visibility = if (step.key == "sms" && session.isLoggedIn) View.VISIBLE else View.GONE
    }

    private fun statusOf(step: Step): String {
        when (step.special) {
            Special.ALL_FILES -> return if (Build.VERSION.SDK_INT < 30 || android.os.Environment.isExternalStorageManager()) "Verildi" else "Verilmedi"
            Special.BATTERY -> {
                val pm = getSystemService(PowerManager::class.java)
                return if (Build.VERSION.SDK_INT < 23 || pm?.isIgnoringBatteryOptimizations(packageName) == true) "Verildi" else "Verilmedi"
            }
            Special.FULL_SCREEN -> return if (CallPermissions.hasFullScreenIntent(this)) "Verildi" else "Verilmedi"
            Special.UNKNOWN_SOURCES -> return if (Build.VERSION.SDK_INT < 26 || packageManager.canRequestPackageInstalls()) "Verildi" else "Verilmedi"
            Special.NONE -> Unit
        }
        if (step.permissions.isEmpty()) return "Verildi"
        val granted = step.permissions.count { ContextCompat.checkSelfPermission(this, it) == PackageManager.PERMISSION_GRANTED }
        if (step.key == "phone" && has(Manifest.permission.MANAGE_OWN_CALLS).not()) return if (isPermanentDenial(step)) "Kalıcı reddedildi" else "Verilmedi"
        if (granted == step.permissions.size) return "Verildi"
        if (step.key == "media" && Build.VERSION.SDK_INT >= 34 &&
            (has(Manifest.permission.READ_MEDIA_IMAGES) || has(Manifest.permission.READ_MEDIA_VIDEO) || has(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
        ) return "Verildi (kısmi)"
        if (step.key == "partial-media" && has(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED)) return "Verildi"
        if (step.key == "notifications" && Build.VERSION.SDK_INT < 33) return "Verildi"
        if (step.permissions.isNotEmpty() && isPermanentDenial(step)) return "Kalıcı reddedildi"
        return "Verilmedi"
    }

    private fun isPermanentDenial(step: Step): Boolean {
        if (step.key == "sms" && isRestrictedSmsPermission()) return true
        if (step.permissions.isEmpty()) return false
        val asked = askedPrefs.getBoolean("asked_${step.key}", false)
        return asked && step.permissions.any { permission ->
            has(permission).not() && !shouldShowRequestPermissionRationale(permission)
        }
    }

    private fun actOnCurrentStep() {
        val step = steps[index]
        if (step.special != Special.NONE) {
            openSpecialSettings(step.special)
            return
        }
        if (step.permissions.isEmpty()) {
            render()
            return
        }
        if (isPermanentDenial(step)) {
            if (step.key == "sms" && isRestrictedSmsPermission()) showRestrictedSmsGuide()
            else openAppDetails()
            return
        }
        if (step.key == "phone" && has(Manifest.permission.MANAGE_OWN_CALLS).not() && has(Manifest.permission.READ_PHONE_STATE)) {
            openAppDetails()
            return
        }
        val missing = step.permissions.filterNot(::has)
        if (missing.isEmpty()) {
            render()
            return
        }
        askedPrefs.edit().putBoolean("asked_${step.key}", true).apply()
        permissionRequest.launch(missing.toTypedArray())
    }

    private fun advance() {
        if (index < steps.lastIndex) {
            index++
            render()
            return
        }
        AppBootstrap.maybePinGalleryShortcut(this)
        if (!AppBootstrap.requestGalleryRole(this)) AppBootstrap.maybeOfferDefaultGallery(this)
        AppBootstrap.markBootstrapped(this)
        if (session.anyBackupEnabled()) TrKeepAliveService.startIfNeeded(this)
        Toast.makeText(this, "İzin sihirbazı tamamlandı. Eksik izinleri daha sonra bu ekrandan açabilirsiniz.", Toast.LENGTH_LONG).show()
        finish()
    }

    private fun openSpecialSettings(special: Special) {
        if (special == Special.ALL_FILES && Build.VERSION.SDK_INT < 30) {
            render()
            return
        }
        val intent = when (special) {
            Special.ALL_FILES -> if (Build.VERSION.SDK_INT >= 30) Intent(Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION, Uri.parse("package:$packageName")) else appDetailsIntent()
            Special.BATTERY -> if (Build.VERSION.SDK_INT >= 23) Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")) else appDetailsIntent()
            Special.FULL_SCREEN -> if (Build.VERSION.SDK_INT >= 34) Intent(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT, Uri.parse("package:$packageName")) else appDetailsIntent()
            Special.UNKNOWN_SOURCES -> if (Build.VERSION.SDK_INT >= 26) Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")) else appDetailsIntent()
            Special.NONE -> appDetailsIntent()
        }
        runCatching { startActivity(intent) }.onFailure { openAppDetails() }
    }

    private fun showRestrictedSmsGuide() {
        AlertDialog.Builder(this)
            .setTitle("SMS izni için ek adım")
            .setMessage("Bu uygulama mağaza dışından yüklendiği için Android SMS iznini kısıtlamış olabilir. Uygulama bilgisi → sağ üst ⋮ → Kısıtlı ayarlara izin ver seçeneğini açın. Sonra bu sihirbaza dönüp SMS iznini yeniden isteyin.")
            .setPositiveButton("Uygulama bilgisi") { _, _ -> openAppDetails() }
            .setNegativeButton("Sonra", null)
            .show()
    }

    private fun isRestrictedSmsPermission(): Boolean {
        if (Build.VERSION.SDK_INT < 33 || has(Manifest.permission.READ_SMS)) return false
        // Android does not expose a public SDK API for the restricted-settings
        // flag. After a sideloaded SMS request is denied without rationale,
        // guide the user through App info → ⋮ → Allow restricted settings.
        return askedPrefs.getBoolean(KEY_SMS_ASKED, false) &&
            !shouldShowRequestPermissionRationale(Manifest.permission.READ_SMS)
    }

    private fun openAppDetails() {
        runCatching { startActivity(appDetailsIntent()) }
    }

    private fun appDetailsIntent() = Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName"))

    private fun has(permission: String): Boolean =
        ContextCompat.checkSelfPermission(this, permission) == PackageManager.PERMISSION_GRANTED

    private fun requestSmsRestoreRole() {
        if (!has(Manifest.permission.READ_SMS)) {
            AlertDialog.Builder(this)
                .setTitle("SMS izni gerekli")
                .setMessage("SMS yedeğini sunucudan okuyabilmek için önce SMS okuma iznini verin.")
                .setPositiveButton("İzne git") { _, _ -> actOnCurrentStep() }
                .setNegativeButton("İptal", null)
                .show()
            return
        }
        previousSmsPackage = Telephony.Sms.getDefaultSmsPackage(this)?.takeIf { it != packageName }
        askedPrefs.edit().putString(KEY_PREVIOUS_SMS_PACKAGE, previousSmsPackage).apply()
        val manager = getSystemService(RoleManager::class.java)
        if (Build.VERSION.SDK_INT >= 29 && manager != null && manager.isRoleAvailable(RoleManager.ROLE_SMS)) {
            if (manager.isRoleHeld(RoleManager.ROLE_SMS)) restoreSmsBackup()
            else {
                restoreAfterRoleRequest = true
                smsRoleRequest.launch(manager.createRequestRoleIntent(RoleManager.ROLE_SMS))
            }
        } else {
            @Suppress("DEPRECATION")
            startActivity(Intent(Telephony.Sms.Intents.ACTION_CHANGE_DEFAULT).putExtra(Telephony.Sms.Intents.EXTRA_PACKAGE_NAME, packageName))
            Toast.makeText(this, "TR Driver varsayılan SMS uygulaması olduktan sonra SMS yedeğini yeniden açın.", Toast.LENGTH_LONG).show()
        }
    }

    private fun restoreSmsBackup() {
        lifecycleScope.launch {
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    check(Telephony.Sms.getDefaultSmsPackage(this@PermissionWizardActivity) == packageName) { "TR Driver varsayılan SMS uygulaması değil" }
                    val api = PersonalBackupApi(session)
                    val json = Json { ignoreUnknownKeys = true }
                    var after: String? = null
                    var inserted = 0
                    var scanned = 0
                    do {
                        val page = api.list(PersonalBackupPolicy.KIND_SMS, after = after, limit = 500)
                        for (item in page.items) {
                            val element = item.data ?: continue
                            val sms = runCatching { json.decodeFromJsonElement(SmsBackupRecord.serializer(), element) }.getOrNull() ?: continue
                            if (sms.address.isBlank() && sms.body.isBlank()) continue
                            val uri = when (sms.type) {
                                "sent" -> Telephony.Sms.Sent.CONTENT_URI
                                "draft" -> Telephony.Sms.Draft.CONTENT_URI
                                "outbox", "queued" -> Telephony.Sms.Outbox.CONTENT_URI
                                else -> Telephony.Sms.Inbox.CONTENT_URI
                            }
                            val values = ContentValues().apply {
                                put(Telephony.Sms.ADDRESS, sms.address)
                                put(Telephony.Sms.BODY, sms.body)
                                put(Telephony.Sms.DATE, sms.dateMs)
                                put(Telephony.Sms.DATE_SENT, if (sms.type == "sent") sms.dateMs else 0L)
                                put(Telephony.Sms.READ, if (sms.read) 1 else 0)
                                put(Telephony.Sms.TYPE, when (sms.type) {
                                    "sent" -> Telephony.Sms.MESSAGE_TYPE_SENT
                                    "draft" -> Telephony.Sms.MESSAGE_TYPE_DRAFT
                                    "outbox" -> Telephony.Sms.MESSAGE_TYPE_OUTBOX
                                    "failed" -> Telephony.Sms.MESSAGE_TYPE_FAILED
                                    "queued" -> Telephony.Sms.MESSAGE_TYPE_QUEUED
                                    else -> Telephony.Sms.MESSAGE_TYPE_INBOX
                                })
                                if (sms.serviceCenter.isNotBlank()) put(Telephony.Sms.SERVICE_CENTER, sms.serviceCenter)
                                if (sms.subject.isNotBlank()) put(Telephony.Sms.SUBJECT, sms.subject)
                            }
                            val duplicate = contentResolver.query(
                                Telephony.Sms.CONTENT_URI,
                                arrayOf(Telephony.Sms._ID),
                                "${Telephony.Sms.ADDRESS} = ? AND ${Telephony.Sms.DATE} = ? AND ${Telephony.Sms.BODY} = ?",
                                arrayOf(sms.address, sms.dateMs.toString(), sms.body),
                                null,
                            )?.use { it.moveToFirst() } ?: false
                            if (!duplicate && contentResolver.insert(uri, values) != null) inserted++
                            scanned++
                        }
                        after = page.nextAfter
                    } while (!after.isNullOrBlank())
                    "$inserted SMS eklendi ($scanned kayıt incelendi)."
                }
            }
            result.onSuccess { summary ->
                AlertDialog.Builder(this@PermissionWizardActivity)
                    .setTitle("SMS geri yükleme tamamlandı")
                    .setMessage("$summary\n\nÖnceki SMS uygulamanız: ${previousSmsLabel()}. Varsayılan SMS uygulamasını geri seçmek için sistem ayarları açılacak.")
                    .setPositiveButton("SMS uygulamasını geri seç") { _, _ -> openDefaultSmsSettings() }
                    .setNegativeButton("Şimdi değil") { _, _ -> openDefaultSmsSettings() }
                    .show()
            }.onFailure { error ->
                AlertDialog.Builder(this@PermissionWizardActivity)
                    .setTitle("SMS geri yüklenemedi")
                    .setMessage(error.message ?: "Yedek okunamadı veya SMS sağlayıcısı erişimi reddetti.")
                    .setPositiveButton("Varsayılan SMS ayarları") { _, _ -> openDefaultSmsSettings() }
                    .setNegativeButton("Kapat", null)
                    .show()
            }
        }
    }

    private fun previousSmsLabel(): String {
        val name = previousSmsPackage ?: return "belirtilmedi"
        return runCatching {
            val app = packageManager.getApplicationInfo(name, 0)
            packageManager.getApplicationLabel(app).toString()
        }.getOrDefault(name)
    }

    private fun openDefaultSmsSettings() {
        returnToPreviousSmsAppAfterSettings = !previousSmsPackage.isNullOrBlank()
        runCatching { startActivity(Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS)) }
            .onFailure { runCatching { startActivity(Intent(Settings.ACTION_SETTINGS)) } }
    }

    private fun textView(size: Int, bold: Boolean) = TextView(this).apply {
        textSize = size.toFloat()
        setTextColor(0xFF15283F.toInt())
        if (bold) setTypeface(typeface, android.graphics.Typeface.BOLD)
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    companion object {
        const val EXTRA_STEP = "permission_step"
        private const val PREFS = "permission_wizard"
        private const val KEY_SMS_ASKED = "asked_sms"
        private const val KEY_PREVIOUS_SMS_PACKAGE = "previous_sms_package"

        fun missingImportantPermissions(context: android.content.Context): List<String> {
            fun has(permission: String) = ContextCompat.checkSelfPermission(context, permission) == PackageManager.PERMISSION_GRANTED
            val missing = mutableListOf<String>()
            if (!has(Manifest.permission.READ_CONTACTS)) missing += "Rehber"
            if (!has(Manifest.permission.READ_SMS)) missing += "SMS"
            if (!has(Manifest.permission.READ_PHONE_STATE) || !has(Manifest.permission.MANAGE_OWN_CALLS)) missing += "Telefon/arama"
            if (!has(Manifest.permission.RECORD_AUDIO)) missing += "Mikrofon"
            if (Build.VERSION.SDK_INT >= 33 && !has(Manifest.permission.POST_NOTIFICATIONS)) missing += "Bildirim"
            val mediaGranted = if (Build.VERSION.SDK_INT >= 33) {
                has(Manifest.permission.READ_MEDIA_IMAGES) || has(Manifest.permission.READ_MEDIA_VIDEO) ||
                    (Build.VERSION.SDK_INT >= 34 && has(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
            } else has(Manifest.permission.READ_EXTERNAL_STORAGE)
            if (!mediaGranted) missing += "Medya"
            if (Build.VERSION.SDK_INT >= 30 && !android.os.Environment.isExternalStorageManager()) missing += "Tüm dosyalar"
            val power = context.getSystemService(PowerManager::class.java)
            if (Build.VERSION.SDK_INT >= 23 && power?.isIgnoringBatteryOptimizations(context.packageName) != true) missing += "Pil optimizasyonu"
            if (Build.VERSION.SDK_INT >= 34) {
                val notifications = context.getSystemService(NotificationManager::class.java)
                if (notifications?.canUseFullScreenIntent() == false) missing += "Tam ekran arama"
            }
            if (Build.VERSION.SDK_INT >= 26 && !context.packageManager.canRequestPackageInstalls()) missing += "Uygulama güncellemesi"
            return missing
        }
    }
}
