package net.neciparmagan.trdriver.calls

import android.Manifest
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.Editable
import android.text.TextWatcher
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.Spinner
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.appcompat.widget.SwitchCompat
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.R
import net.neciparmagan.trdriver.data.CommsAccess
import net.neciparmagan.trdriver.data.SessionStore

class CallsActivity : AppCompatActivity() {

    private lateinit var session: SessionStore
    private lateinit var configApi: LocalTelConfigApi

    private lateinit var tvHeaderStatus: TextView
    private lateinit var btnReturnActiveCall: Button
    private lateinit var tabDialer: Button
    private lateinit var tabSettings: Button
    private lateinit var panelDialer: View
    private lateinit var panelSettings: View
    private lateinit var tvLocalTelConfigStatus: TextView
    private lateinit var layoutAdvancedLocalTel: View
    private lateinit var btnAdvancedLocalTel: Button

    private lateinit var cardPermissions: View
    private lateinit var tvPermissionsSummary: TextView
    private lateinit var btnFixMicPerm: Button
    private lateinit var btnFixContactsPerm: Button
    private lateinit var btnFixFsiPerm: Button

    private lateinit var etDialTarget: EditText
    private lateinit var tvDialMatchPreview: TextView
    private lateinit var etContactSearch: EditText
    private lateinit var tvContactsEmpty: TextView
    private lateinit var rvContacts: RecyclerView

    private lateinit var swEnabled: SwitchCompat
    private lateinit var rgProvider: RadioGroup
    private lateinit var rbSip: RadioButton
    private lateinit var rbGateway: RadioButton
    private lateinit var etServerUrl: EditText
    private lateinit var etSipDomain: EditText
    private lateinit var etSipPort: EditText
    private lateinit var spTransport: Spinner
    private lateinit var etUsername: EditText
    private lateinit var etExtension: EditText
    private lateinit var etDisplayName: EditText
    private lateinit var etSecret: EditText
    private lateinit var etOutboundPrefix: EditText
    private lateinit var etCountryCode: EditText
    private lateinit var swAutoRegister: SwitchCompat
    private lateinit var swSyncToServer: SwitchCompat

    private var contactIndex: ContactIndex = ContactMatcher.buildIndex(emptyList())
    private val contactsAdapter = ContactsCallAdapter { entry, rawTarget ->
        initiateCallWithPermissions(rawTarget, entry.displayName)
    }
    private val transportValues = listOf(
        LocalTelTransport.UDP,
        LocalTelTransport.TCP,
        LocalTelTransport.TLS,
        LocalTelTransport.WSS,
        LocalTelTransport.HTTPS,
    )

    private var pendingDialTarget: Pair<String, String>? = null
    private val mainHandler = Handler(Looper.getMainLooper())

    private val micPermLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            refreshPermissionBanner()
            val pending = pendingDialTarget
            pendingDialTarget = null
            if (granted && pending != null) {
                initiateCallWithPermissions(pending.first, pending.second)
            } else if (!granted) {
                Toast.makeText(this, "Mikrofon izni verilmedi; ses iletilemez", Toast.LENGTH_SHORT).show()
            }
        }

    private val contactsPermLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            refreshPermissionBanner()
            if (granted) {
                AndroidContactDirectory.invalidate()
                reloadContactsAsync(forceRefresh = true)
            }
        }

    private val notifPermLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) {
            refreshPermissionBanner()
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_calls)

        val root = findViewById<View>(R.id.callsRoot)
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            insets
        }

        session = SessionStore(this)
        configApi = LocalTelConfigApi(session)
        CallController.init(this)

        bindViews()
        setupTransportSpinner()
        loadConfigIntoForm(session.loadLocalTelConfig())
        wireListeners()
        observeStates()

        reloadContactsAsync(forceRefresh = false)
        val currentConfig = session.loadLocalTelConfig()
        if (session.isLoggedIn && (currentConfig.providerType == LocalTelProviderType.GATEWAY || !currentConfig.enabled)) {
            lifecycleScope.launch {
                configApi.fetchGatewayConfig().onSuccess { autoConfig ->
                    session.saveLocalTelConfig(autoConfig)
                    loadConfigIntoForm(autoConfig)
                    CallController.ensureProviderStarted(this@CallsActivity, forceRestart = true)
                    LocalTelListenerService.syncState(this@CallsActivity)
                }
            }
        } else if (session.localTelEnabled && session.localTelAutoRegister) {
            LocalTelListenerService.syncState(this)
        }
    }

    override fun onResume() {
        super.onResume()
        refreshPermissionBanner()
        if (CommsAccess.hasContactsAccess(this) && contactIndex.size() == 0) {
            reloadContactsAsync(forceRefresh = false)
        }
    }

    private fun bindViews() {
        tvHeaderStatus = findViewById(R.id.tvLocalTelHeaderStatus)
        btnReturnActiveCall = findViewById(R.id.btnReturnActiveCall)
        tabDialer = findViewById(R.id.tabDialerContacts)
        tabSettings = findViewById(R.id.tabSettings)
        panelDialer = findViewById(R.id.panelDialerContacts)
        panelSettings = findViewById(R.id.panelLocalTelSettings)
        tvLocalTelConfigStatus = findViewById(R.id.tvLocalTelConfigStatus)
        layoutAdvancedLocalTel = findViewById(R.id.layoutAdvancedLocalTel)
        btnAdvancedLocalTel = findViewById(R.id.btnAdvancedLocalTel)

        cardPermissions = findViewById(R.id.cardPermissions)
        tvPermissionsSummary = findViewById(R.id.tvPermissionsSummary)
        btnFixMicPerm = findViewById(R.id.btnFixMicPerm)
        btnFixContactsPerm = findViewById(R.id.btnFixContactsPerm)
        btnFixFsiPerm = findViewById(R.id.btnFixFsiPerm)

        etDialTarget = findViewById(R.id.etDialTarget)
        tvDialMatchPreview = findViewById(R.id.tvDialMatchPreview)
        etContactSearch = findViewById(R.id.etContactSearch)
        tvContactsEmpty = findViewById(R.id.tvContactsEmpty)
        rvContacts = findViewById(R.id.rvCallContacts)
        rvContacts.layoutManager = LinearLayoutManager(this)
        rvContacts.adapter = contactsAdapter

        swEnabled = findViewById(R.id.swLocalTelEnabled)
        rgProvider = findViewById(R.id.rgProviderType)
        rbSip = findViewById(R.id.rbProviderSip)
        rbGateway = findViewById(R.id.rbProviderGateway)
        etServerUrl = findViewById(R.id.etServerUrl)
        etSipDomain = findViewById(R.id.etSipDomain)
        etSipPort = findViewById(R.id.etSipPort)
        spTransport = findViewById(R.id.spTransport)
        etUsername = findViewById(R.id.etSipUsername)
        etExtension = findViewById(R.id.etExtension)
        etDisplayName = findViewById(R.id.etDisplayName)
        etSecret = findViewById(R.id.etSipSecret)
        etOutboundPrefix = findViewById(R.id.etOutboundPrefix)
        etCountryCode = findViewById(R.id.etCountryCode)
        swAutoRegister = findViewById(R.id.swAutoRegister)
        swSyncToServer = findViewById(R.id.swSyncToServer)
    }

    private fun setupTransportSpinner() {
        val labels = listOf("UDP", "TCP", "TLS", "WSS", "HTTPS")
        val adapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, labels)
        adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        spTransport.adapter = adapter
    }

    private fun wireListeners() {
        findViewById<Button>(R.id.btnBackCalls).setOnClickListener { finish() }

        btnReturnActiveCall.setOnClickListener {
            val state = CallController.currentState()
            if (state.isLive) {
                startActivity(
                    Intent(this, CallActivity::class.java).apply {
                        putExtra(CallActivity.EXTRA_CALL_ID, state.callId)
                    },
                )
            }
        }

        tabDialer.setOnClickListener { selectTab(showSettings = false) }
        tabSettings.setOnClickListener { selectTab(showSettings = true) }
        btnAdvancedLocalTel.setOnClickListener {
            val show = layoutAdvancedLocalTel.visibility != View.VISIBLE
            layoutAdvancedLocalTel.visibility = if (show) View.VISIBLE else View.GONE
            btnAdvancedLocalTel.text = if (show) "Gelişmiş ayarları gizle" else "Gelişmiş ayarları göster"
        }
        val etMyPhone = findViewById<EditText>(R.id.etMyPhone)
        val tvMyExtension = findViewById<TextView>(R.id.tvMyExtension)
        fun showMyPhone(result: Result<Pair<String, String>>) {
            result.onSuccess { (phone, ext) ->
                tvMyExtension.text = if (ext.isBlank()) "Dahili numaram: -" else "Dahili numaram: $ext"
                if (phone.isNotBlank() && etMyPhone.text.isNullOrBlank()) etMyPhone.setText("+$phone")
            }
        }
        if (configApi.hasSession()) {
            lifecycleScope.launch { showMyPhone(configApi.getMyPhone()) }
        }
        findViewById<Button>(R.id.btnSaveMyPhone).setOnClickListener {
            val typed = etMyPhone.text?.toString().orEmpty()
            lifecycleScope.launch {
                configApi.setMyPhone(typed).onSuccess { (phone, ext) ->
                    showMyPhone(Result.success(phone to ext))
                    etMyPhone.setText(if (phone.isBlank()) "" else "+$phone")
                    Toast.makeText(
                        this@CallsActivity,
                        if (phone.isBlank()) "Numara silindi" else "Numaran kaydedildi; artık seni numaranla arayabilirler",
                        Toast.LENGTH_LONG,
                    ).show()
                }.onFailure {
                    Toast.makeText(this@CallsActivity, it.message ?: "Numara kaydedilemedi", Toast.LENGTH_LONG).show()
                }
            }
        }
        findViewById<Button>(R.id.btnReconnectLocalTel).setOnClickListener {
            reconnectLocalTel()
        }

        btnFixMicPerm.setOnClickListener {
            CallPermissions.showMicrophoneExplanation(this) {
                micPermLauncher.launch(Manifest.permission.RECORD_AUDIO)
            }
        }
        btnFixContactsPerm.setOnClickListener {
            CallPermissions.showContactsExplanation(this) {
                contactsPermLauncher.launch(Manifest.permission.READ_CONTACTS)
            }
        }
        btnFixFsiPerm.setOnClickListener {
            when {
                !CallPermissions.hasNotifications(this) && Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU -> {
                    CallPermissions.showNotificationsExplanation(this) {
                        notifPermLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                    }
                }
                !CallPermissions.hasFullScreenIntent(this) -> {
                    CallPermissions.showFullScreenIntentExplanation(this) {
                        CallPermissions.openFullScreenIntentSettings(this)
                    }
                }
            }
        }

        etDialTarget.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit
            override fun afterTextChanged(s: Editable?) {
                updateDialPreview(s?.toString().orEmpty())
            }
        })

        findViewById<Button>(R.id.btnCallNow).setOnClickListener {
            val raw = etDialTarget.text?.toString().orEmpty().trim()
            if (raw.isEmpty()) {
                Toast.makeText(this, "Aramak için numara veya dahili girin", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            val cfg = session.loadLocalTelConfig()
            val match = contactIndex.match(raw, cfg.defaultCountryCode)
            initiateCallWithPermissions(raw, match?.displayName.orEmpty())
        }

        findViewById<Button>(R.id.btnTestIncomingRing).setOnClickListener {
            val testNumber = etDialTarget.text?.toString()?.trim().orEmpty().ifEmpty { "05551112233" }
            if (!CallPermissions.hasFullScreenIntent(this)) {
                CallPermissions.showFullScreenIntentExplanation(this) {
                    CallPermissions.openFullScreenIntentSettings(this)
                }
                return@setOnClickListener
            }
            Toast.makeText(
                this,
                "4 saniye içinde gelen test araması çalacak — ekranı kilitleyerek test edebilirsiniz",
                Toast.LENGTH_LONG,
            ).show()
            mainHandler.postDelayed({
                CallController.triggerTestIncomingCall(applicationContext, testNumber, "LocalTel Test")
            }, 4_000L)
        }

        findViewById<Button>(R.id.btnRefreshContacts).setOnClickListener {
            if (!CommsAccess.hasContactsAccess(this)) {
                CallPermissions.showContactsExplanation(this) {
                    contactsPermLauncher.launch(Manifest.permission.READ_CONTACTS)
                }
            } else {
                AndroidContactDirectory.invalidate()
                reloadContactsAsync(forceRefresh = true)
            }
        }

        etContactSearch.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit
            override fun afterTextChanged(s: Editable?) {
                filterContacts(s?.toString().orEmpty())
            }
        })

        rgProvider.setOnCheckedChangeListener { _, checkedId ->
            if (checkedId == R.id.rbProviderGateway) {
                if (etSipPort.text.toString().trim() == "5060" || etSipPort.text.isNullOrBlank()) {
                    etSipPort.setText("443")
                }
                spTransport.setSelection(transportValues.indexOf(LocalTelTransport.WSS))
            } else {
                if (etSipPort.text.toString().trim() == "443" || etSipPort.text.isNullOrBlank()) {
                    etSipPort.setText("5060")
                }
                if (spTransport.selectedItemPosition >= 3) {
                    spTransport.setSelection(transportValues.indexOf(LocalTelTransport.UDP))
                }
            }
        }

        findViewById<Button>(R.id.btnSaveLocalTel).setOnClickListener {
            saveConfiguration()
        }

        findViewById<Button>(R.id.btnFetchServerConfig).setOnClickListener {
            fetchConfigurationFromServer()
        }

        findViewById<Button>(R.id.btnClearLocalTel).setOnClickListener {
            AlertDialog.Builder(this)
                .setTitle("LocalTel ayarlarını temizle")
                .setMessage("Cihazdaki (ve bağlıysa sunucudaki) LocalTel kimlik bilgileri silinsin mi?")
                .setPositiveButton("Temizle") { _, _ ->
                    val wasSynced = session.localTelSyncToServer
                    session.clearLocalTelConfig()
                    LocalTelListenerService.stop(this)
                    CallController.disconnectProvider()
                    loadConfigIntoForm(LocalTelConfig())
                    if (wasSynced && configApi.hasSession()) {
                        lifecycleScope.launch { configApi.deleteConfig() }
                    }
                    Toast.makeText(this, "LocalTel ayarları temizlendi", Toast.LENGTH_SHORT).show()
                }
                .setNegativeButton("İptal", null)
                .show()
        }
    }

    private fun observeStates() {
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                launch {
                    CallController.providerStatus.collect { snap ->
                        val prefix = if (session.localTelEnabled) snap.statusText else "LocalTel kapalı"
                        tvHeaderStatus.text = prefix
                        refreshLocalTelStatus(snap)
                    }
                }
                launch {
                    CallController.callState.collect { state ->
                        btnReturnActiveCall.visibility = if (state.isLive) View.VISIBLE else View.GONE
                    }
                }
            }
        }
    }

    private fun refreshLocalTelStatus(snap: ProviderStatusSnapshot = CallController.providerStatus.value) {
        val text = when (snap.state) {
            ProviderRegistrationState.REGISTERED -> "Bağlı"
            ProviderRegistrationState.REGISTERING -> "Bağlanıyor"
            ProviderRegistrationState.ERROR -> "Hata: ${snap.lastError.ifBlank { snap.statusText }}"
            ProviderRegistrationState.DISABLED -> if (session.localTelEnabled) "Hata: bağlantı kapalı" else "Bağlanıyor: hesap bekleniyor"
        }
        tvLocalTelConfigStatus.text = text
    }

    private fun reconnectLocalTel() {
        val config = session.loadLocalTelConfig()
        if (config.providerType == LocalTelProviderType.GATEWAY && configApi.hasSession()) {
            lifecycleScope.launch {
                tvLocalTelConfigStatus.text = "Bağlanıyor"
                configApi.fetchGatewayConfig().onSuccess { refreshed ->
                    session.saveLocalTelConfig(refreshed)
                    CallController.ensureProviderStarted(this@CallsActivity, forceRestart = true)
                    LocalTelListenerService.syncState(this@CallsActivity)
                }.onFailure {
                    tvLocalTelConfigStatus.text = "Hata: sunucu ayarları alınamadı"
                    CallController.ensureProviderStarted(this@CallsActivity, forceRestart = true)
                }
            }
        } else {
            CallController.ensureProviderStarted(this, forceRestart = true)
            LocalTelListenerService.syncState(this)
        }
    }

    private fun selectTab(showSettings: Boolean) {
        panelDialer.visibility = if (showSettings) View.GONE else View.VISIBLE
        panelSettings.visibility = if (showSettings) View.VISIBLE else View.GONE
        tabDialer.setBackgroundTintList(
            ContextCompat.getColorStateList(this, if (showSettings) R.color.tr_sky else R.color.tr_blue),
        )
        tabDialer.setTextColor(
            ContextCompat.getColor(this, if (showSettings) R.color.tr_ink else R.color.tr_white),
        )
        tabSettings.setBackgroundTintList(
            ContextCompat.getColorStateList(this, if (showSettings) R.color.tr_blue else R.color.tr_sky),
        )
        tabSettings.setTextColor(
            ContextCompat.getColor(this, if (showSettings) R.color.tr_white else R.color.tr_ink),
        )
    }

    private fun refreshPermissionBanner() {
        val missingMic = !CallPermissions.hasMicrophone(this)
        val missingContacts = !CommsAccess.hasContactsAccess(this)
        val missingNotif = !CallPermissions.hasNotifications(this)
        val missingFsi = !CallPermissions.hasFullScreenIntent(this)

        btnFixMicPerm.visibility = if (missingMic) View.VISIBLE else View.GONE
        btnFixContactsPerm.visibility = if (missingContacts) View.VISIBLE else View.GONE
        btnFixFsiPerm.visibility = if (missingNotif || missingFsi) View.VISIBLE else View.GONE
        btnFixFsiPerm.text = if (missingNotif) "Bildirim" else "Kilit Ekranı"

        val missingNames = buildList {
            if (missingMic) add("Mikrofon (sesli arama)")
            if (missingContacts) add("Rehber (arayan adı eşleştirme)")
            if (missingNotif) add("Bildirim")
            if (missingFsi) add("Kilit ekranında tam ekran arama")
        }
        if (missingNames.isEmpty()) {
            cardPermissions.visibility = View.GONE
        } else {
            cardPermissions.visibility = View.VISIBLE
            tvPermissionsSummary.text = "Eksik izinler: ${missingNames.joinToString(", ")}"
        }
    }

    private fun reloadContactsAsync(forceRefresh: Boolean) {
        if (!CommsAccess.hasContactsAccess(this)) {
            tvContactsEmpty.text = "Kişi adlarını görmek ve rehberden aramak için yukarıdan Rehber izni verin."
            tvContactsEmpty.visibility = View.VISIBLE
            contactsAdapter.submitList(emptyList())
            return
        }
        lifecycleScope.launch {
            val defaultCc = session.localTelCountryCode
            val idx = withContext(Dispatchers.IO) {
                AndroidContactDirectory.loadIndex(applicationContext, defaultCc, forceRefresh)
            }
            contactIndex = idx
            filterContacts(etContactSearch.text?.toString().orEmpty())
            updateDialPreview(etDialTarget.text?.toString().orEmpty())
        }
    }

    private fun filterContacts(query: String) {
        if (!CommsAccess.hasContactsAccess(this)) return
        val rows = contactIndex.search(query, limit = 150)
        contactsAdapter.submitList(rows)
        tvContactsEmpty.visibility = if (rows.isEmpty()) View.VISIBLE else View.GONE
        if (rows.isEmpty()) {
            tvContactsEmpty.text = "Eşleşen rehber kaydı bulunamadı."
        }
    }

    private fun updateDialPreview(raw: String) {
        val trimmed = raw.trim()
        if (trimmed.isEmpty()) {
            tvDialMatchPreview.visibility = View.GONE
            return
        }
        val cc = session.localTelCountryCode
        val match = contactIndex.match(trimmed, cc)
        val norm = PhoneNumberNormalizer.normalize(trimmed, cc)
        tvDialMatchPreview.visibility = View.VISIBLE
        tvDialMatchPreview.text = if (match != null) {
            buildString {
                append("Rehber eşleşmesi: ").append(match.displayName)
                if (match.numberLabel.isNotBlank()) append(" (").append(match.numberLabel).append(")")
                if (norm.displayNumber.isNotBlank()) append(" · ").append(norm.displayNumber)
            }
        } else {
            "Aranacak hedef: ${norm.displayNumber.ifBlank { trimmed }}"
        }
    }

    private fun initiateCallWithPermissions(rawTarget: String, displayName: String) {
        if (!CallPermissions.hasMicrophone(this)) {
            pendingDialTarget = rawTarget to displayName
            CallPermissions.showMicrophoneExplanation(this) {
                micPermLauncher.launch(Manifest.permission.RECORD_AUDIO)
            }
            return
        }
        val cfg = session.loadLocalTelConfig()
        if (!cfg.isReady(session.serverUrl)) {
            Toast.makeText(this, "Önce LocalTel ayarlarını yapılandırın", Toast.LENGTH_SHORT).show()
            selectTab(showSettings = true)
            return
        }
        val res = CallController.startOutgoingCall(this, rawTarget, displayName)
        if (res.isFailure) {
            Toast.makeText(
                this,
                res.exceptionOrNull()?.message ?: "Arama başlatılamadı",
                Toast.LENGTH_LONG,
            ).show()
        }
    }

    private fun loadConfigIntoForm(cfg: LocalTelConfig) {
        swEnabled.isChecked = cfg.enabled
        if (cfg.providerType == LocalTelProviderType.GATEWAY) {
            rbGateway.isChecked = true
        } else {
            rbSip.isChecked = true
        }
        etServerUrl.setText(cfg.serverUrl)
        etSipDomain.setText(cfg.sipDomain)
        etSipPort.setText(cfg.sipPort.toString())
        val trIdx = transportValues.indexOf(cfg.transport).coerceAtLeast(0)
        spTransport.setSelection(trIdx)
        etUsername.setText(cfg.username)
        etExtension.setText(cfg.extension)
        etDisplayName.setText(cfg.displayName)
        etSecret.setText("")
        etSecret.hint = if (cfg.secret.isNotBlank()) {
            "Kayıtlı parola mevcut (değiştirmek için yazın)"
        } else {
            "SIP Parolası / Token"
        }
        etOutboundPrefix.setText(cfg.outboundPrefix)
        etCountryCode.setText(cfg.defaultCountryCode.ifBlank { "90" })
        swAutoRegister.isChecked = cfg.autoRegister
        swSyncToServer.isChecked = cfg.syncToServer
    }

    private fun saveConfiguration() {
        val provider = if (rbGateway.isChecked) LocalTelProviderType.GATEWAY else LocalTelProviderType.SIP
        val transport = transportValues.getOrElse(spTransport.selectedItemPosition) { LocalTelTransport.UDP }
        val port = etSipPort.text?.toString()?.trim()?.toIntOrNull()?.coerceIn(1, 65535)
            ?: if (provider == LocalTelProviderType.GATEWAY) 443 else 5060

        val existingSecret = session.localTelSecret
        val typedSecret = etSecret.text?.toString().orEmpty()
        val finalSecret = typedSecret.ifEmpty { existingSecret }

        val config = LocalTelConfig(
            enabled = swEnabled.isChecked,
            providerType = provider,
            serverUrl = etServerUrl.text?.toString().orEmpty().trim(),
            sipDomain = etSipDomain.text?.toString().orEmpty().trim(),
            sipPort = port,
            transport = transport,
            username = etUsername.text?.toString().orEmpty().trim(),
            extension = etExtension.text?.toString().orEmpty().trim(),
            displayName = etDisplayName.text?.toString().orEmpty().trim(),
            secret = finalSecret,
            outboundPrefix = etOutboundPrefix.text?.toString().orEmpty().trim(),
            defaultCountryCode = etCountryCode.text?.toString().orEmpty().filter { it in '0'..'9' }.ifEmpty { "90" },
            autoRegister = swAutoRegister.isChecked,
            syncToServer = swSyncToServer.isChecked,
        )

        if (config.enabled && !config.isReady(session.serverUrl)) {
            val msg = if (provider == LocalTelProviderType.SIP) {
                "SIP için sunucu adresi ve kullanıcı adı girin"
            } else {
                "Ağ geçidi URL'si girin veya TR Driver hesabına giriş yapın"
            }
            Toast.makeText(this, msg, Toast.LENGTH_LONG).show()
            return
        }

        session.saveLocalTelConfig(config)
        if (config.enabled) {
            CallController.ensureProviderStarted(this, forceRestart = true)
            LocalTelListenerService.syncState(this)
        } else {
            LocalTelListenerService.stop(this)
            CallController.disconnectProvider()
        }

        if (config.syncToServer && configApi.hasSession()) {
            lifecycleScope.launch {
                val res = configApi.saveConfig(config)
                if (res.isSuccess) {
                    Toast.makeText(
                        this@CallsActivity,
                        "LocalTel ayarları cihaza ve sunucuya şifreli kaydedildi",
                        Toast.LENGTH_SHORT,
                    ).show()
                } else {
                    Toast.makeText(
                        this@CallsActivity,
                        "Cihaza kaydedildi; sunucu eşitlemesi başarısız: ${res.exceptionOrNull()?.message}",
                        Toast.LENGTH_LONG,
                    ).show()
                }
            }
        } else {
            Toast.makeText(this, "LocalTel ayarları cihazda güvenli kaydedildi", Toast.LENGTH_SHORT).show()
        }
        etSecret.setText("")
        etSecret.hint = if (finalSecret.isNotBlank()) {
            "Kayıtlı parola mevcut (değiştirmek için yazın)"
        } else {
            "SIP Parolası / Token"
        }
    }

    private fun fetchConfigurationFromServer() {
        if (!configApi.hasSession()) {
            Toast.makeText(this, "Önce TR Driver hesabına giriş yapın", Toast.LENGTH_SHORT).show()
            return
        }
        lifecycleScope.launch {
            val res = configApi.fetchGatewayConfig().recoverCatching {
                configApi.provisionConfig().getOrThrow()
            }
            res.onSuccess { loaded ->
                session.saveLocalTelConfig(loaded)
                loadConfigIntoForm(loaded)
                if (loaded.enabled) {
                    CallController.ensureProviderStarted(this@CallsActivity, forceRestart = true)
                    LocalTelListenerService.syncState(this@CallsActivity)
                }
                Toast.makeText(this@CallsActivity, "LocalTel ayarları sunucudan yüklendi", Toast.LENGTH_SHORT).show()
            }.onFailure { err ->
                Toast.makeText(
                    this@CallsActivity,
                    err.message ?: "Sunucudan ayar yüklenemedi",
                    Toast.LENGTH_LONG,
                ).show()
            }
        }
    }
}

private class ContactsCallAdapter(
    private val onCallClick: (ContactDirectoryEntry, String) -> Unit,
) : RecyclerView.Adapter<ContactsCallAdapter.VH>() {

    private var items: List<ContactDirectoryEntry> = emptyList()

    fun submitList(newItems: List<ContactDirectoryEntry>) {
        items = newItems
        notifyDataSetChanged()
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
        val view = LayoutInflater.from(parent.context)
            .inflate(R.layout.item_call_contact, parent, false)
        return VH(view)
    }

    override fun onBindViewHolder(holder: VH, position: Int) {
        val item = items[position]
        val title = item.displayName.ifBlank { item.phones.firstOrNull()?.rawNumber.orEmpty() }
        val primaryTarget = item.phones.firstOrNull()?.rawNumber
            ?: item.sipAddresses.firstOrNull().orEmpty()
        val subtitle = buildString {
            item.phones.firstOrNull()?.let { p ->
                append(PhoneNumberNormalizer.formatForDisplay(p.rawNumber))
                if (p.label.isNotBlank()) append(" · ").append(p.label)
            }
            if (item.phones.isEmpty() && item.sipAddresses.isNotEmpty()) {
                append(item.sipAddresses.first())
            }
        }

        holder.avatar.text = title.trim().firstOrNull()?.uppercaseChar()?.toString() ?: "?"
        holder.name.text = title
        holder.subtitle.text = subtitle
        holder.btnCall.isEnabled = primaryTarget.isNotBlank()
        holder.btnCall.setOnClickListener {
            if (primaryTarget.isNotBlank()) {
                onCallClick(item, primaryTarget)
            }
        }
        holder.itemView.setOnClickListener {
            val allTargets = item.phones.map { p ->
                val disp = PhoneNumberNormalizer.formatForDisplay(p.rawNumber)
                val lbl = if (p.label.isNotBlank()) "$disp (${p.label})" else disp
                lbl to p.rawNumber
            } + item.sipAddresses.map { s -> "LocalTel · $s" to s }

            if (allTargets.size <= 1) {
                if (primaryTarget.isNotBlank()) onCallClick(item, primaryTarget)
            } else {
                val labels = allTargets.map { it.first }.toTypedArray()
                AlertDialog.Builder(holder.itemView.context)
                    .setTitle(title)
                    .setItems(labels) { _, which ->
                        onCallClick(item, allTargets[which].second)
                    }
                    .setNegativeButton("İptal", null)
                    .show()
            }
        }
    }

    override fun getItemCount(): Int = items.size

    class VH(v: View) : RecyclerView.ViewHolder(v) {
        val avatar: TextView = v.findViewById(R.id.tvContactAvatar)
        val name: TextView = v.findViewById(R.id.tvContactName)
        val subtitle: TextView = v.findViewById(R.id.tvContactSubtitle)
        val btnCall: Button = v.findViewById(R.id.btnContactCall)
    }
}
