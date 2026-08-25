package net.neciparmagan.trdriver

import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.CachedUpload
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UriCacheCopy

/**
 * Bluetooth / Dosyalar / Paylaş menüsünden gelen resim ve PDF'leri
 * açık klasöre veya araç kabul plakasına yükler.
 *
 * Kritik: paylaşılan content:// URI izinleri kısa ömürlüdür;
 * diyalogdan önce cache'e kopyalanır.
 */
class ShareImportActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var api: DriveApi
    private lateinit var status: TextView
    private lateinit var bar: ProgressBar
    private var pending: List<CachedUpload> = emptyList()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_share_import)
        session = SessionStore(this)
        api = DriveApi(session, this)
        status = findViewById(R.id.shareStatus)
        bar = findViewById(R.id.shareBar)
        findViewById<Button>(R.id.btnShareClose).setOnClickListener {
            cleanupPending()
            finish()
        }

        if (!session.isLoggedIn) {
            status.text = "Önce TR Driver'a giriş yapın"
            Toast.makeText(this, "Önce giriş yapın", Toast.LENGTH_LONG).show()
            startActivity(Intent(this, MainActivity::class.java))
            finish()
            return
        }

        val uris = collectUris(intent)
        if (uris.isEmpty()) {
            status.text = "Paylaşılan dosya yok"
            return
        }

        status.text = "${uris.size} dosya kopyalanıyor…"
        bar.visibility = View.VISIBLE
        bar.isIndeterminate = true

        lifecycleScope.launch {
            pending = withContext(Dispatchers.IO) {
                uris.mapNotNull { uri ->
                    runCatching {
                        UriCacheCopy.copyToCache(this@ShareImportActivity, uri)
                    }.getOrNull()
                }
            }
            bar.visibility = View.GONE
            bar.isIndeterminate = false
            if (pending.isEmpty()) {
                status.text = "Dosyalar kopyalanamadı (Bluetooth/izin)"
                Toast.makeText(this@ShareImportActivity, status.text, Toast.LENGTH_LONG).show()
                return@launch
            }
            status.text = "${pending.size} dosya hazır — hedef seçin"
            showTargetDialog()
        }
    }

    private fun showTargetDialog() {
        val plate = session.lastIntakePlate
        val browseId = session.lastBrowseFolderId.ifBlank { session.storageRootId }
        val browseLabel = if (session.lastBrowseFolderId.isNotBlank()) "Son açık klasör" else "Dosyalarım (kök)"
        val options = buildList {
            add("$browseLabel'e yükle")
            if (plate.isNotBlank()) add("Araç kabul · $plate")
            add("Araç kabul (plaka seç / incele)")
        }

        AlertDialog.Builder(this)
            .setTitle("${pending.size} dosya nereye eklensin?")
            .setItems(options.toTypedArray()) { _, which ->
                when {
                    which == 0 -> uploadCached(browseId, browseLabel)
                    plate.isNotBlank() && which == 1 -> uploadToIntake(plate)
                    else -> {
                        val uris = ArrayList(pending.map { it.uri })
                        startActivity(
                            Intent(this, VehicleIntakeActivity::class.java).apply {
                                putParcelableArrayListExtra(VehicleIntakeActivity.EXTRA_SHARE_URIS, uris)
                            },
                        )
                        // VehicleIntake owns / will re-copy; keep files until activity reads them.
                        // Clear our list without deleting — intake copies again from FileProvider.
                        pending = emptyList()
                        finish()
                    }
                }
            }
            .setOnCancelListener {
                cleanupPending()
                finish()
            }
            .show()
    }

    private fun collectUris(intent: Intent): List<Uri> {
        val list = mutableListOf<Uri>()
        when (intent.action) {
            Intent.ACTION_SEND -> {
                val uri = if (Build.VERSION.SDK_INT >= 33) {
                    intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
                } else {
                    @Suppress("DEPRECATION")
                    intent.getParcelableExtra(Intent.EXTRA_STREAM)
                }
                if (uri != null) list.add(uri)
            }
            Intent.ACTION_SEND_MULTIPLE -> {
                val many = if (Build.VERSION.SDK_INT >= 33) {
                    intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java)
                } else {
                    @Suppress("DEPRECATION")
                    intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM)
                }
                if (many != null) list.addAll(many.filterNotNull())
            }
        }
        return list
    }

    private fun uploadToIntake(plate: String) {
        status.text = "Klasör hazırlanıyor · $plate"
        bar.visibility = View.VISIBLE
        bar.isIndeterminate = true
        lifecycleScope.launch {
            try {
                val folder = withContext(Dispatchers.IO) { api.ensureVehicleIntakeFolder(plate) }
                session.rememberIntakePlate(folder.name, folder.id)
                uploadCached(folder.id, "TR Araç Kabul / ${folder.name}")
            } catch (e: Exception) {
                bar.visibility = View.GONE
                status.text = "Hata: ${e.message}"
                Toast.makeText(this@ShareImportActivity, e.message, Toast.LENGTH_LONG).show()
            }
        }
    }

    private fun uploadCached(parentId: String, label: String) {
        val items = pending
        if (items.isEmpty()) {
            status.text = "Yüklenecek dosya yok"
            return
        }
        status.text = "Yükleniyor…"
        bar.visibility = View.VISIBLE
        bar.isIndeterminate = false
        bar.max = 100
        bar.progress = 0
        lifecycleScope.launch {
            var ok = 0
            var fail = 0
            try {
                for ((index, item) in items.withIndex()) {
                    status.text = "Yükleniyor ${index + 1}/${items.size} · ${item.displayName}"
                    try {
                        withContext(Dispatchers.IO) {
                            api.upload(
                                parentId,
                                item.uri,
                                onProgress = { sent, total ->
                                    val filePct = if (total > 0) ((sent * 100) / total).toInt() else 0
                                    val overall = ((index * 100) + filePct) / items.size
                                    runOnUiThread { bar.progress = overall.coerceIn(0, 99) }
                                },
                            )
                        }
                        ok++
                    } catch (_: Exception) {
                        fail++
                    }
                }
                bar.progress = 100
                status.text = when {
                    fail == 0 -> "Tamam · $ok dosya → $label"
                    ok == 0 -> "Yükleme başarısız"
                    else -> "$ok yüklendi, $fail hata → $label"
                }
                Toast.makeText(this@ShareImportActivity, status.text, Toast.LENGTH_LONG).show()
            } finally {
                cleanupPending()
            }
        }
    }

    private fun cleanupPending() {
        pending.forEach { it.localFile?.delete() }
        pending = emptyList()
    }

    override fun onDestroy() {
        if (isFinishing) cleanupPending()
        super.onDestroy()
    }
}
