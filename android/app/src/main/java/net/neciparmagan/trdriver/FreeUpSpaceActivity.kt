package net.neciparmagan.trdriver

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.provider.MediaStore
import android.view.View
import android.widget.Button
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.IntentSenderRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.FreeUpCandidate
import net.neciparmagan.trdriver.data.FreeUpSpace
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.UploadedMediaDb

/**
 * Google Photos-style "Free up space":
 * 1) List locally present backed-up items
 * 2) Verify each remote file exists (retries on network blips)
 * 3) Delete local only after verification — never deletes remote
 */
class FreeUpSpaceActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var api: DriveApi
    private lateinit var db: UploadedMediaDb
    private lateinit var summary: TextView
    private lateinit var status: TextView
    private lateinit var bar: ProgressBar
    private lateinit var btnScan: Button
    private lateinit var btnFree: Button

    private var planCandidates: List<FreeUpCandidate> = emptyList()
    private var pendingDelete: List<FreeUpCandidate> = emptyList()
    private var busy = false

    private val deleteRequestLauncher =
        registerForActivityResult(ActivityResultContracts.StartIntentSenderForResult()) { result ->
            lifecycleScope.launch {
                if (result.resultCode == Activity.RESULT_OK) {
                    finalizeAfterSystemDelete(pendingDelete)
                } else {
                    status.text = "Silme iptal edildi — telefonda dosyalar duruyor"
                    setBusy(false)
                }
                pendingDelete = emptyList()
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_free_up_space)
        session = SessionStore(this)
        api = DriveApi(session, this)
        db = UploadedMediaDb(this)

        if (!session.isLoggedIn) {
            Toast.makeText(this, "Önce giriş yapın", Toast.LENGTH_LONG).show()
            finish()
            return
        }

        summary = findViewById(R.id.freeUpSummary)
        status = findViewById(R.id.freeUpStatus)
        bar = findViewById(R.id.freeUpBar)
        btnScan = findViewById(R.id.btnScanFreeUp)
        btnFree = findViewById(R.id.btnVerifyAndFree)

        btnScan.setOnClickListener { scan() }
        btnFree.setOnClickListener { confirmAndFree() }
        findViewById<Button>(R.id.btnOpenPhotos).setOnClickListener {
            startActivity(Intent(this, PhotosLibraryActivity::class.java))
        }
        findViewById<Button>(R.id.btnCloseFreeUp).setOnClickListener { finish() }

        refreshIdleSummary()
    }

    private fun refreshIdleSummary() {
        val notFreed = db.countNotFreed()
        val bytes = db.sumNotFreedBytes()
        summary.text =
            "Kayıtlı yedek: ${db.countUploaded()} dosya\n" +
                "Henüz yer açılmamış: $notFreed · ~${FreeUpSpace.formatBytes(bytes)}\n" +
                "Aday listesi için “Adayları tara”ya basın."
        status.text = "Güvenli silme: her dosya silinmeden önce sunucuda kontrol edilir."
        btnFree.isEnabled = false
        planCandidates = emptyList()
    }

    private fun scan() {
        if (busy) return
        if (!FreeUpSpace.hasNetwork(this)) {
            Toast.makeText(this, "İnternet gerekli (Wi‑Fi veya mobil)", Toast.LENGTH_LONG).show()
            return
        }
        setBusy(true)
        status.text = "Yerel dosyalar taranıyor…"
        lifecycleScope.launch {
            try {
                val plan = withContext(Dispatchers.IO) { FreeUpSpace.buildPlan(this@FreeUpSpaceActivity, db) }
                planCandidates = plan.candidates
                val bytes = plan.candidates.sumOf { it.row.sizeBytes }
                summary.text =
                    "Silinebilir aday: ${plan.candidates.size} dosya · ~${FreeUpSpace.formatBytes(bytes)}\n" +
                        "Zaten telefonda yok (işaretlendi): ${plan.alreadyGone}\n" +
                        "URI eksik (eski kayıt, atlandı): ${plan.missingUri}"
                status.text = if (plan.candidates.isEmpty()) {
                    "Yer açılacak dosya yok. Önce galeri yedeği çalıştırın."
                } else {
                    "Hazır. “Doğrula ve yer aç” sunucuyu kontrol edip siler."
                }
                btnFree.isEnabled = plan.candidates.isNotEmpty()
            } catch (e: Exception) {
                status.text = "Tarama hatası: ${e.message}"
                Toast.makeText(this@FreeUpSpaceActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                setBusy(false)
            }
        }
    }

    private fun confirmAndFree() {
        if (busy || planCandidates.isEmpty()) return
        val bytes = planCandidates.sumOf { it.row.sizeBytes }
        AlertDialog.Builder(this)
            .setTitle("Telefondan silinsin mi?")
            .setMessage(
                "${planCandidates.size} dosya (~${FreeUpSpace.formatBytes(bytes)}) " +
                    "yalnızca sunucuda doğrulandıktan sonra telefonda silinecek.\n\n" +
                    "• Sunucudaki kopyalar silinmez\n" +
                    "• Ağ koparsa işlem durur; veri kaybı olmaz\n" +
                    "• Sonra TR Photos’tan görüntüleyebilirsiniz",
            )
            .setPositiveButton("Doğrula ve sil") { _, _ -> runVerifyAndDelete() }
            .setNegativeButton("İptal", null)
            .show()
    }

    private fun runVerifyAndDelete() {
        if (busy) return
        setBusy(true)
        bar.visibility = View.VISIBLE
        bar.isIndeterminate = false
        bar.progress = 0
        status.text = "Sunucu doğrulaması…"
        lifecycleScope.launch {
            try {
                if (!FreeUpSpace.hasNetwork(this@FreeUpSpaceActivity)) {
                    status.text = "Ağ yok — silme yapılmadı"
                    return@launch
                }
                val verify = FreeUpSpace.verifyOnServer(
                    this@FreeUpSpaceActivity,
                    api,
                    planCandidates,
                ) { done, total, _ ->
                    runOnUiThread {
                        bar.progress = if (total > 0) (done * 100) / total else 0
                        status.text = "Doğrulanıyor $done / $total"
                    }
                }
                if (verify.verified.isEmpty()) {
                    status.text =
                        "Silinecek doğrulanmış dosya yok " +
                            "(sunucuda yok: ${verify.remoteMissing}, ağ: ${verify.networkErrors})"
                    return@launch
                }
                status.text =
                    "${verify.verified.size} doğrulandı · siliniyor… " +
                        "(atlandı: yok=${verify.remoteMissing}, ağ=${verify.networkErrors})"

                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    pendingDelete = verify.verified
                    val uris = verify.verified.map { it.uri }
                    val request = MediaStore.createDeleteRequest(contentResolver, uris)
                    deleteRequestLauncher.launch(IntentSenderRequest.Builder(request.intentSender).build())
                    // busy stays true until launcher returns
                    return@launch
                }

                val deleted = withContext(Dispatchers.IO) {
                    FreeUpSpace.deleteDirect(this@FreeUpSpaceActivity, verify.verified)
                }
                bar.progress = 100
                val freedBytes = verify.verified
                    .filter { it.row.mediaKey in deleted }
                    .sumOf { it.row.sizeBytes }
                status.text =
                    "Tamam · ${deleted.size} dosya silindi · ~${FreeUpSpace.formatBytes(freedBytes)} açıldı"
                Toast.makeText(this@FreeUpSpaceActivity, status.text, Toast.LENGTH_LONG).show()
                planCandidates = emptyList()
                btnFree.isEnabled = false
                refreshIdleSummary()
                status.text =
                    "Tamam · ${deleted.size} dosya silindi · ~${FreeUpSpace.formatBytes(freedBytes)} açıldı"
            } catch (e: Exception) {
                status.text = "Durdu (güvenli): ${e.message}"
                Toast.makeText(this@FreeUpSpaceActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                if (pendingDelete.isEmpty()) setBusy(false)
            }
        }
    }

    private suspend fun finalizeAfterSystemDelete(items: List<FreeUpCandidate>) {
        withContext(Dispatchers.IO) {
            val stillThere = ArrayList<String>()
            val gone = ArrayList<String>()
            for (item in items) {
                if (FreeUpSpace.localExists(this@FreeUpSpaceActivity, item.uri)) {
                    stillThere += item.row.mediaKey
                } else {
                    gone += item.row.mediaKey
                }
            }
            db.markFreedMany(gone)
            // If somehow still present, don't mark freed — user can retry.
            val freedBytes = items.filter { it.row.mediaKey in gone }.sumOf { it.row.sizeBytes }
            runOnUiThread {
                bar.progress = 100
                status.text =
                    "Tamam · ${gone.size} silindi · ~${FreeUpSpace.formatBytes(freedBytes)} açıldı" +
                        if (stillThere.isNotEmpty()) " · ${stillThere.size} duruyor" else ""
                Toast.makeText(this@FreeUpSpaceActivity, status.text, Toast.LENGTH_LONG).show()
                planCandidates = emptyList()
                btnFree.isEnabled = false
                refreshIdleSummary()
                status.text =
                    "Tamam · ${gone.size} silindi · ~${FreeUpSpace.formatBytes(freedBytes)} açıldı"
                setBusy(false)
            }
        }
    }

    private fun setBusy(value: Boolean) {
        busy = value
        btnScan.isEnabled = !value
        btnFree.isEnabled = !value && planCandidates.isNotEmpty()
        bar.visibility = if (value) View.VISIBLE else View.GONE
    }
}
