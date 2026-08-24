package net.neciparmagan.trdriver

import android.Manifest
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.net.Uri
import android.os.Bundle
import android.provider.OpenableColumns
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.ImageView
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.SessionStore
import java.io.File

/**
 * Oto servis araç kabul: plaka klasörü + fotoğraf kuyruğu + hızlı sunucu yükleme.
 * Sunucu yolu: TR Araç Kabul / {PLAKA}/
 */
class VehicleIntakeActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var api: DriveApi

    private lateinit var inputPlate: EditText
    private lateinit var folderStatus: TextView
    private lateinit var photoCount: TextView
    private lateinit var uploadBar: ProgressBar
    private lateinit var uploadStatus: TextView
    private lateinit var btnPrepareFolder: Button
    private lateinit var btnCapture: Button
    private lateinit var btnPickGallery: Button
    private lateinit var btnSend: Button
    private lateinit var photoList: RecyclerView

    private var folderId: String? = null
    private var folderName: String = ""
    private val photos = mutableListOf<PendingPhoto>()
    private var uploading = false

    private var pendingCaptureFile: File? = null
    private var pendingCaptureUri: Uri? = null

    private val cameraPermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (granted) launchCamera()
        else Toast.makeText(this, "Kamera izni gerekli", Toast.LENGTH_LONG).show()
    }

    private val takePicture = registerForActivityResult(ActivityResultContracts.TakePicture()) { ok ->
        val uri = pendingCaptureUri
        val file = pendingCaptureFile
        pendingCaptureUri = null
        pendingCaptureFile = null
        if (!ok || uri == null) {
            file?.delete()
            return@registerForActivityResult
        }
        photos.add(
            PendingPhoto(
                uri = uri,
                displayName = file?.name ?: "foto_${System.currentTimeMillis()}.jpg",
                localFile = file,
            ),
        )
        refreshList()
        Toast.makeText(this, "Eklendi (${photos.size})", Toast.LENGTH_SHORT).show()
    }

    private val pickImages = registerForActivityResult(
        ActivityResultContracts.GetMultipleContents(),
    ) { uris ->
        if (uris.isEmpty()) return@registerForActivityResult
        for (uri in uris) {
            photos.add(
                PendingPhoto(
                    uri = uri,
                    displayName = queryDisplayName(uri) ?: "galeri_${System.currentTimeMillis()}.jpg",
                    localFile = null,
                ),
            )
        }
        refreshList()
        Toast.makeText(this, "${uris.size} dosya eklendi", Toast.LENGTH_SHORT).show()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_vehicle_intake)
        session = SessionStore(this)
        api = DriveApi(session, this)

        if (!session.isLoggedIn) {
            Toast.makeText(this, "Önce giriş yapın", Toast.LENGTH_LONG).show()
            finish()
            return
        }

        inputPlate = findViewById(R.id.inputPlate)
        folderStatus = findViewById(R.id.folderStatus)
        photoCount = findViewById(R.id.photoCount)
        uploadBar = findViewById(R.id.uploadBar)
        uploadStatus = findViewById(R.id.uploadStatus)
        btnPrepareFolder = findViewById(R.id.btnPrepareFolder)
        btnCapture = findViewById(R.id.btnCapture)
        btnPickGallery = findViewById(R.id.btnPickGallery)
        btnSend = findViewById(R.id.btnSend)
        photoList = findViewById(R.id.photoList)

        photoList.layoutManager = LinearLayoutManager(this)
        photoList.adapter = PhotoAdapter(photos) { index ->
            if (uploading) return@PhotoAdapter
            photos.removeAt(index).localFile?.delete()
            refreshList()
        }

        btnPrepareFolder.setOnClickListener { prepareFolder() }
        btnCapture.setOnClickListener { ensureCameraAndCapture() }
        btnPickGallery.setOnClickListener { pickImages.launch("image/*") }
        btnSend.setOnClickListener { sendAll() }
        findViewById<Button>(R.id.btnClose).setOnClickListener { finish() }

        refreshList()
    }

    private fun prepareFolder() {
        val plate = inputPlate.text?.toString().orEmpty().trim()
        if (plate.isBlank()) {
            Toast.makeText(this, "Plaka yazın", Toast.LENGTH_SHORT).show()
            return
        }
        btnPrepareFolder.isEnabled = false
        folderStatus.text = "Klasör hazırlanıyor…"
        lifecycleScope.launch {
            try {
                val entry = withContext(Dispatchers.IO) { api.ensureVehicleIntakeFolder(plate) }
                folderId = entry.id
                folderName = entry.name
                folderStatus.text = "Hazır: TR Araç Kabul / ${entry.name}"
                setCaptureEnabled(true)
                Toast.makeText(this@VehicleIntakeActivity, "Klasör hazır", Toast.LENGTH_SHORT).show()
            } catch (e: Exception) {
                folderStatus.text = "Hata: ${e.message}"
                Toast.makeText(this@VehicleIntakeActivity, e.message ?: "Klasör açılamadı", Toast.LENGTH_LONG).show()
            } finally {
                btnPrepareFolder.isEnabled = true
            }
        }
    }

    private fun ensureCameraAndCapture() {
        if (folderId.isNullOrBlank()) {
            Toast.makeText(this, "Önce klasörü hazırlayın", Toast.LENGTH_SHORT).show()
            return
        }
        when {
            ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) ==
                PackageManager.PERMISSION_GRANTED -> launchCamera()
            else -> cameraPermission.launch(Manifest.permission.CAMERA)
        }
    }

    private fun launchCamera() {
        try {
            val dir = File(cacheDir, "intake").also { it.mkdirs() }
            val file = File(dir, "IMG_${System.currentTimeMillis()}.jpg")
            val uri = FileProvider.getUriForFile(this, "$packageName.files", file)
            pendingCaptureFile = file
            pendingCaptureUri = uri
            takePicture.launch(uri)
        } catch (e: Exception) {
            Toast.makeText(this, "Kamera açılamadı: ${e.message}", Toast.LENGTH_LONG).show()
        }
    }

    private fun sendAll() {
        val parent = folderId
        if (parent.isNullOrBlank()) {
            Toast.makeText(this, "Önce klasörü hazırlayın", Toast.LENGTH_SHORT).show()
            return
        }
        if (photos.isEmpty()) {
            Toast.makeText(this, "En az bir fotoğraf ekleyin", Toast.LENGTH_SHORT).show()
            return
        }
        if (uploading) return
        uploading = true
        setCaptureEnabled(false)
        btnPrepareFolder.isEnabled = false
        btnSend.isEnabled = false
        uploadBar.visibility = View.VISIBLE
        uploadStatus.visibility = View.VISIBLE
        uploadBar.progress = 0

        val queue = photos.toList()
        lifecycleScope.launch {
            var ok = 0
            var fail = 0
            try {
                for ((index, photo) in queue.withIndex()) {
                    val n = index + 1
                    uploadStatus.text = "Yükleniyor $n / ${queue.size} · ${photo.displayName}"
                    uploadBar.progress = ((index * 100) / queue.size).coerceIn(0, 99)
                    try {
                        withContext(Dispatchers.IO) {
                            api.upload(
                                parent,
                                photo.uri,
                                onProgress = { sent, total ->
                                    val filePct = if (total > 0) ((sent * 100) / total).toInt() else 0
                                    val overall = ((index * 100) + filePct) / queue.size
                                    runOnUiThread {
                                        uploadBar.progress = overall.coerceIn(0, 99)
                                        uploadStatus.text =
                                            "$n / ${queue.size} · ${photo.displayName} · " +
                                                "${SessionStore.formatBytes(sent)} / ${SessionStore.formatBytes(total)}"
                                    }
                                },
                                onRetry = { attempt, _ ->
                                    runOnUiThread {
                                        uploadStatus.text =
                                            "Ağ değişti, yeniden deneniyor ($attempt) · ${photo.displayName}"
                                    }
                                },
                            )
                        }
                        ok++
                        photo.localFile?.delete()
                    } catch (e: Exception) {
                        fail++
                        uploadStatus.text = "Hata ($n): ${e.message}"
                    }
                }
                photos.clear()
                refreshList()
                uploadBar.progress = 100
                val msg = when {
                    fail == 0 -> "Tamam · $ok fotoğraf → TR Araç Kabul / $folderName"
                    ok == 0 -> "Yükleme başarısız ($fail hata)"
                    else -> "$ok yüklendi, $fail hata · TR Araç Kabul / $folderName"
                }
                uploadStatus.text = msg
                Toast.makeText(this@VehicleIntakeActivity, msg, Toast.LENGTH_LONG).show()
            } finally {
                uploading = false
                btnPrepareFolder.isEnabled = true
                setCaptureEnabled(folderId != null)
                refreshList()
            }
        }
    }

    private fun setCaptureEnabled(enabled: Boolean) {
        btnCapture.isEnabled = enabled && !uploading
        btnPickGallery.isEnabled = enabled && !uploading
    }

    private fun refreshList() {
        photoCount.text = "${photos.size} fotoğraf"
        btnSend.isEnabled = !uploading && folderId != null && photos.isNotEmpty()
        photoList.adapter?.notifyDataSetChanged()
    }

    private fun queryDisplayName(uri: Uri): String? {
        contentResolver.query(uri, null, null, null, null)?.use { cursor ->
            val idx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            if (cursor.moveToFirst() && idx >= 0) return cursor.getString(idx)
        }
        return null
    }

    private data class PendingPhoto(
        val uri: Uri,
        val displayName: String,
        val localFile: File?,
    )

    private class PhotoAdapter(
        private val items: List<PendingPhoto>,
        private val onRemove: (Int) -> Unit,
    ) : RecyclerView.Adapter<PhotoAdapter.VH>() {
        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val thumb: ImageView = view.findViewById(R.id.thumb)
            val name: TextView = view.findViewById(R.id.photoName)
            val remove: Button = view.findViewById(R.id.btnRemove)
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
            val view = LayoutInflater.from(parent.context)
                .inflate(R.layout.item_intake_photo, parent, false)
            return VH(view)
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(holder: VH, position: Int) {
            val item = items[position]
            holder.name.text = item.displayName
            holder.remove.setOnClickListener {
                val pos = holder.bindingAdapterPosition
                if (pos != RecyclerView.NO_POSITION) onRemove(pos)
            }
            holder.thumb.setImageDrawable(null)
            holder.thumb.post {
                runCatching {
                    val ctx = holder.thumb.context
                    ctx.contentResolver.openInputStream(item.uri)?.use { input ->
                        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                        BitmapFactory.decodeStream(input, null, bounds)
                    }
                    ctx.contentResolver.openInputStream(item.uri)?.use { input ->
                        val opts = BitmapFactory.Options().apply {
                            inSampleSize = 8
                        }
                        val bmp = BitmapFactory.decodeStream(input, null, opts)
                        holder.thumb.setImageBitmap(bmp)
                    }
                }
            }
        }
    }
}
