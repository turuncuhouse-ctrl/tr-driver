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
 * 1) Only rows in UploadedMediaDb with remote_id (never raw gallery scan)
 * 2) Local size must still match backup-time size (blocks MediaStore ID reuse)
 * 3) Live remote exists check (+ final recheck before delete)
 * 4) Never deletes remote; never marks freed on permission ambiguity
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
        findViewById<Button>(R.id.btnOpenDrive).setOnClickListener {
            startActivity(
                Intent(this, MainActivity::class.java).apply {
                    flags = Intent.FLAG_ACTIVITY_REORDER_TO_FRONT or Intent.FLAG_ACTIVITY_CLEAR_TOP
                },
            )
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
                "Aday listesi için “Adayları tara”ya basın.\n\n" +
                "Kural: yedeklenmemiş hiçbir dosya silinmez."
        status.text =
            "Güvenli silme: DB kaydı + boyut eşleşmesi + sunucu doğrulaması. " +
                "Yedeklenmeyenler aday bile olmaz."
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
        status.text = "Yalnızca yedek kayıtları taranıyor (tüm galeri değil)…"
        lifecycleScope.launch {
            try {
                val plan = withContext(Dispatchers.IO) { FreeUpSpace.buildPlan(this@FreeUpSpaceActivity, db) }
                planCandidates = plan.candidates
                val bytes = plan.candidates.sumOf { it.row.sizeBytes }
                summary.text =
                    "Silinebilir aday: ${plan.candidates.size} dosya · ~${FreeUpSpace.formatBytes(bytes)}\n" +
                        "Zaten telefonda yok (işaretlendi): ${plan.alreadyGone}\n" +
                        "URI eksik (atlandı): ${plan.missingUri}\n" +
                        "Boyut uyuşmaz (atlandı, silinmez): ${plan.sizeMismatch}\n" +
                        "İzin belirsiz (atlandı, silinmez): ${plan.presenceUnknown}"
                status.text = if (plan.candidates.isEmpty()) {
                    "Yer açılacak güvenli dosya yok. Önce Wi‑Fi yedeği çalışsın."
                } else {
                    "Hazır. “Doğrula ve yer aç” sunucuyu iki kez kontrol edip siler.\n" +
                        FreeUpSpace.previewNames(plan.candidates)
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
        val preview = FreeUpSpace.previewNames(planCandidates)
        AlertDialog.Builder(this)
            .setTitle("Telefondan silinsin mi?")
            .setMessage(
                "${planCandidates.size} dosya (~${FreeUpSpace.formatBytes(bytes)}) " +
                    "yalnızca sunucuda doğrulandıktan sonra telefonda silinecek.\n\n" +
                    "• Yedeklenmemiş dosyalar listede yoktur\n" +
                    "• Sunucudaki kopyalar silinmez\n" +
                    "• Boyutu değişen / şüpheli kayıtlar atlanır\n" +
                    "• Ağ koparsa işlem durur\n\n" +
                    preview,
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
                ) { done, total, name ->
                    runOnUiThread {
                        bar.progress = if (total > 0) (done * 100) / total else 0
                        status.text = "Doğrulanıyor $done / $total · $name"
                    }
                }
                if (verify.verified.isEmpty()) {
                    status.text =
                        "Silinecek doğrulanmış dosya yok " +
                            "(sunucuda yok: ${verify.remoteMissing}, ağ: ${verify.networkErrors}, " +
                            "boyut: ${verify.sizeMismatch}, izin: ${verify.presenceUnknown})"
                    return@launch
                }

                status.text = "Son kontrol (silmeden hemen önce)…"
                val finalList = withContext(Dispatchers.IO) {
                    FreeUpSpace.recheckBeforeDelete(this@FreeUpSpaceActivity, api, verify.verified)
                }
                if (finalList.isEmpty()) {
                    status.text = "Son kontrolde güvenli dosya kalmadı — hiçbir şey silinmedi"
                    return@launch
                }

                status.text =
                    "${finalList.size} doğrulandı · siliniyor… " +
                        "(atlandı: yok=${verify.remoteMissing}, ağ=${verify.networkErrors}, " +
                        "boyut=${verify.sizeMismatch})"

                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    pendingDelete = finalList
                    val uris = finalList.map { it.uri }
                    val request = MediaStore.createDeleteRequest(contentResolver, uris)
                    deleteRequestLauncher.launch(IntentSenderRequest.Builder(request.intentSender).build())
                    return@launch
                }

                val deleted = withContext(Dispatchers.IO) {
                    FreeUpSpace.deleteDirect(this@FreeUpSpaceActivity, finalList)
                }
                bar.progress = 100
                val freedBytes = finalList
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
                when (FreeUpSpace.localPresence(this@FreeUpSpaceActivity, item.uri)) {
                    net.neciparmagan.trdriver.data.LocalPresence.GONE -> gone += item.row.mediaKey
                    else -> stillThere += item.row.mediaKey
                }
            }
            db.markFreedMany(gone)
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
