package net.neciparmagan.trdriver

import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.provider.OpenableColumns
import android.text.InputType
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.ListView
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.HttpStatusIOException
import net.neciparmagan.trdriver.data.MimeHelper
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.SharedEntry
import net.neciparmagan.trdriver.data.SharedSpaceApi
import net.neciparmagan.trdriver.data.SharedSpacePaths
import java.io.File

/**
 * Browser for the server-wide "Ortak Paylaşım" folder. The server decides what
 * this account may see; folders without a grant simply do not appear, and the
 * write actions are offered only where the server reported write access.
 */
class SharedSpaceActivity : AppCompatActivity() {
    private lateinit var api: SharedSpaceApi
    private lateinit var pathView: TextView
    private lateinit var quotaView: TextView
    private lateinit var statusView: TextView
    private lateinit var progress: ProgressBar
    private lateinit var list: ListView
    private lateinit var btnUp: Button
    private lateinit var btnUpload: Button
    private lateinit var btnNewFolder: Button
    private lateinit var adapter: EntryAdapter

    private var currentPath = ""
    private var currentLevel = "none"
    private var spaceFull = false
    private var busy = false

    private val picker = registerForActivityResult(ActivityResultContracts.GetContent()) { uri ->
        if (uri != null) startUpload(uri)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_shared_space)
        val session = SessionStore(this)
        if (!session.isLoggedIn) {
            Toast.makeText(this, "Önce giriş yapın", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        api = SharedSpaceApi(session, applicationContext)

        pathView = findViewById(R.id.sharedPath)
        quotaView = findViewById(R.id.sharedQuota)
        statusView = findViewById(R.id.sharedStatus)
        progress = findViewById(R.id.sharedProgress)
        list = findViewById(R.id.sharedList)
        btnUp = findViewById(R.id.btnSharedUp)
        btnUpload = findViewById(R.id.btnSharedUpload)
        btnNewFolder = findViewById(R.id.btnSharedNewFolder)

        adapter = EntryAdapter()
        list.adapter = adapter
        list.setOnItemClickListener { _, _, position, _ ->
            val entry = adapter.getItem(position) ?: return@setOnItemClickListener
            if (entry.isDir) load(entry.path) else download(entry)
        }
        list.setOnItemLongClickListener { _, _, position, _ ->
            val entry = adapter.getItem(position)
            if (entry != null) showEntryActions(entry)
            true
        }
        btnUp.setOnClickListener { load(SharedSpacePaths.parent(currentPath)) }
        btnUpload.setOnClickListener { picker.launch("*/*") }
        btnNewFolder.setOnClickListener { promptNewFolder() }

        onBackPressedDispatcher.addCallback(
            this,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    if (currentPath.isEmpty()) finish() else load(SharedSpacePaths.parent(currentPath))
                }
            },
        )

        if (savedInstanceState != null) {
            currentPath = savedInstanceState.getString(STATE_PATH, "")
        }
        load(currentPath)
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putString(STATE_PATH, currentPath)
    }

    private fun setBusy(value: Boolean) {
        busy = value
        progress.visibility = if (value) View.VISIBLE else View.INVISIBLE
        updateButtons()
    }

    private fun updateButtons() {
        val writable = SharedSpacePaths.canWrite(currentLevel)
        btnUp.isEnabled = !busy && currentPath.isNotEmpty()
        btnUpload.isEnabled = !busy && writable && !spaceFull
        btnNewFolder.isEnabled = !busy && writable
        btnUpload.visibility = if (writable) View.VISIBLE else View.GONE
        btnNewFolder.visibility = if (writable) View.VISIBLE else View.GONE
    }

    private fun showStatus(text: String?) {
        statusView.text = text.orEmpty()
        statusView.visibility = if (text.isNullOrBlank()) View.GONE else View.VISIBLE
    }

    private fun describe(error: Throwable): String {
        if (error is HttpStatusIOException) {
            return when (error.code) {
                401 -> "Oturum süresi dolmuş. Yeniden giriş yapın."
                403 -> "Bu klasör için izniniz yok."
                404 -> "Dosya veya klasör bulunamadı."
                413 -> "Dosya, sunucunun izin verdiği boyuttan büyük."
                429 -> "Çok fazla istek. Biraz bekleyip yeniden deneyin."
                503 -> "Ortak Paylaşım bu sunucuda kullanılamıyor."
                507 -> "Ortak Paylaşım alanı dolu."
                else -> error.message ?: "İstek başarısız"
            }
        }
        return error.message ?: "Sunucuya ulaşılamadı"
    }

    /** Runs [block] with the busy indicator on and reports failures in the status line. */
    private fun launchTask(block: suspend () -> Unit) {
        if (busy) return
        setBusy(true)
        lifecycleScope.launch {
            try {
                block()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                showStatus(describe(e))
            } finally {
                setBusy(false)
            }
        }
    }

    private fun load(path: String) = launchTask { refresh(path) }

    private suspend fun refresh(path: String) {
        val status = api.status()
        if (status.unavailable) {
            showEmpty("Ortak Paylaşım bu sunucuda kullanılamıyor.")
            return
        }
        if (!status.access.available) {
            showEmpty("Ortak Paylaşım'da size açılmış bir klasör yok. Erişim için sunucu yöneticinize başvurun.")
            return
        }
        val listing = api.list(path)
        val entries = listing.entries.orEmpty()
        currentPath = listing.path
        currentLevel = listing.level
        val quota = status.quota
        spaceFull = quota?.level == "full"
        pathView.text = if (currentPath.isEmpty()) "/" else "/$currentPath"
        if (quota != null) {
            val free = SharedSpacePaths.formatSize(quota.freeBytes.coerceAtLeast(0))
            quotaView.text = when (quota.level) {
                "full" -> "Alan dolu — yeni dosya yüklenemez"
                "warning" -> "Alan dolmak üzere · $free boş"
                else -> "${SharedSpacePaths.formatSize(quota.usedBytes)} kullanılıyor · $free boş"
            }
            quotaView.visibility = View.VISIBLE
        } else {
            quotaView.visibility = View.GONE
        }
        adapter.clear()
        adapter.addAll(entries)
        showStatus(if (entries.isEmpty()) "Bu klasör boş." else null)
        updateButtons()
    }

    private fun showEmpty(message: String) {
        currentPath = ""
        currentLevel = "none"
        spaceFull = false
        pathView.text = "/"
        quotaView.visibility = View.GONE
        adapter.clear()
        showStatus(message)
        updateButtons()
    }

    private fun download(entry: SharedEntry) = launchTask {
        showStatus("İndiriliyor: ${entry.name}")
        val file = api.downloadToCache(entry)
        showStatus(null)
        open(file)
    }

    private fun open(file: File) {
        val uri = try {
            FileProvider.getUriForFile(this, "$packageName.files", file)
        } catch (e: Exception) {
            showStatus("Dosya açılamadı: ${e.message}")
            return
        }
        val extension = file.name.substringAfterLast('.', "")
        val mime = MimeHelper.guessFromExtension(extension) ?: "*/*"
        val view = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mime)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        for (info in packageManager.queryIntentActivities(view, PackageManager.MATCH_DEFAULT_ONLY)) {
            grantUriPermission(info.activityInfo.packageName, uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        try {
            startActivity(Intent.createChooser(view, "Dosyayı aç").addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION))
        } catch (e: Exception) {
            showStatus("Bu dosya türünü açabilecek bir uygulama bulunamadı.")
        }
    }

    private fun showEntryActions(entry: SharedEntry) {
        val labels = ArrayList<String>()
        val actions = ArrayList<() -> Unit>()
        if (entry.isDir) {
            labels.add("Aç")
            actions.add { load(entry.path) }
        } else {
            labels.add("İndir ve aç")
            actions.add { download(entry) }
        }
        // Renaming or deleting an entry changes the folder that contains it.
        if (SharedSpacePaths.canWrite(currentLevel)) {
            labels.add("Yeniden adlandır")
            actions.add { promptRename(entry) }
            labels.add("Sil")
            actions.add { confirmDelete(entry) }
        }
        AlertDialog.Builder(this)
            .setTitle(entry.name)
            .setItems(labels.toTypedArray()) { _, which -> actions[which]() }
            .setNegativeButton("Vazgeç", null)
            .show()
    }

    private fun promptName(title: String, initial: String, onName: (String) -> Unit) {
        val input = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT
            setText(initial)
            setSelection(text.length)
        }
        AlertDialog.Builder(this)
            .setTitle(title)
            .setView(input)
            .setPositiveButton("Tamam") { _, _ ->
                val name = input.text.toString().trim()
                if (SharedSpacePaths.isValidName(name)) {
                    onName(name)
                } else {
                    showStatus("Geçersiz ad. Ad boş olamaz ve / veya \\ içeremez.")
                }
            }
            .setNegativeButton("Vazgeç", null)
            .show()
    }

    private fun promptNewFolder() = promptName("Yeni klasör", "") { name ->
        val dir = currentPath
        launchTask {
            api.createFolder(dir, name)
            refresh(dir)
        }
    }

    private fun promptRename(entry: SharedEntry) = promptName("Yeniden adlandır", entry.name) { name ->
        if (name == entry.name) return@promptName
        val dir = currentPath
        launchTask {
            api.rename(entry.path, name)
            refresh(dir)
        }
    }

    private fun confirmDelete(entry: SharedEntry) {
        val what = if (entry.isDir) "\"${entry.name}\" klasörü ve içindeki her şey" else "\"${entry.name}\""
        AlertDialog.Builder(this)
            .setTitle("Kalıcı olarak silinsin mi?")
            .setMessage("$what Ortak Paylaşım'dan herkes için silinecek. Bu işlem geri alınamaz.")
            .setPositiveButton("Sil") { _, _ ->
                val dir = currentPath
                launchTask {
                    api.delete(entry.path)
                    refresh(dir)
                }
            }
            .setNegativeButton("Vazgeç", null)
            .show()
    }

    private fun startUpload(uri: Uri) {
        var name = ""
        var size = -1L
        try {
            contentResolver.query(uri, null, null, null, null)?.use { cursor ->
                if (cursor.moveToFirst()) {
                    val nameIndex = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    val sizeIndex = cursor.getColumnIndex(OpenableColumns.SIZE)
                    if (nameIndex >= 0) name = cursor.getString(nameIndex).orEmpty()
                    if (sizeIndex >= 0 && !cursor.isNull(sizeIndex)) size = cursor.getLong(sizeIndex)
                }
            }
        } catch (e: Exception) {
            // Some providers refuse the query; fall back to the URI's last segment.
        }
        if (name.isBlank()) name = uri.lastPathSegment?.substringAfterLast('/').orEmpty()
        if (!SharedSpacePaths.isValidName(name)) {
            showStatus("Seçilen dosyanın adı okunamadı.")
            return
        }
        upload(uri, name, size, overwrite = false)
    }

    private fun upload(uri: Uri, name: String, size: Long, overwrite: Boolean) {
        val dir = currentPath
        if (busy) return
        setBusy(true)
        showStatus("Yükleniyor: $name")
        lifecycleScope.launch {
            try {
                api.upload(dir, name, uri, size, overwrite)
                refresh(dir)
                showStatus("\"$name\" yüklendi.")
            } catch (e: CancellationException) {
                throw e
            } catch (e: HttpStatusIOException) {
                if (e.code == 409 && !overwrite) {
                    showStatus(null)
                    confirmOverwrite(uri, name, size)
                } else {
                    showStatus(describe(e))
                }
            } catch (e: Exception) {
                showStatus(describe(e))
            } finally {
                setBusy(false)
            }
        }
    }

    private fun confirmOverwrite(uri: Uri, name: String, size: Long) {
        AlertDialog.Builder(this)
            .setTitle("Dosya zaten var")
            .setMessage("\"$name\" bu klasörde zaten var. Üzerine yazılsın mı?")
            .setPositiveButton("Üzerine yaz") { _, _ -> upload(uri, name, size, overwrite = true) }
            .setNegativeButton("Vazgeç", null)
            .show()
    }

    private inner class EntryAdapter :
        ArrayAdapter<SharedEntry>(this@SharedSpaceActivity, android.R.layout.simple_list_item_2, android.R.id.text1, ArrayList<SharedEntry>()) {
        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val view = super.getView(position, convertView, parent)
            val entry = getItem(position) ?: return view
            val title = view.findViewById<TextView>(android.R.id.text1)
            val detail = view.findViewById<TextView>(android.R.id.text2)
            title.text = if (entry.isDir) "📁 ${entry.name}" else entry.name
            detail.text = when {
                entry.isDir && entry.level == "none" -> "Klasör · geçiş"
                entry.isDir && entry.level == "read" -> "Klasör · salt okunur"
                entry.isDir -> "Klasör"
                else -> SharedSpacePaths.formatSize(entry.size)
            }
            return view
        }
    }

    private companion object {
        const val STATE_PATH = "shared_space_path"
    }
}
