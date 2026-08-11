package net.neciparmagan.trdriver

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.presentation.DriveViewModel
import java.io.File

class MainActivity : AppCompatActivity() {
    private val vm: DriveViewModel by viewModels()

    private lateinit var loginPanel: View
    private lateinit var filesPanel: View
    private lateinit var progress: ProgressBar
    private lateinit var crumbs: TextView
    private lateinit var list: RecyclerView
    private lateinit var titleEmail: TextView

    private val adapter = FileAdapter(
        onOpen = { entry ->
            if (entry.kind == "folder") vm.openFolder(entry) else vm.download(entry)
        },
        onDownload = { vm.download(it) },
        onDelete = { entry ->
            AlertDialog.Builder(this)
                .setMessage("\"${entry.name}\" silinsin mi?")
                .setPositiveButton("Sil") { _, _ -> vm.deleteEntry(entry) }
                .setNegativeButton("İptal", null)
                .show()
        },
    )

    private val picker = registerForActivityResult(ActivityResultContracts.GetContent()) { uri: Uri? ->
        if (uri != null) vm.upload(uri)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        loginPanel = findViewById(R.id.loginPanel)
        filesPanel = findViewById(R.id.filesPanel)
        progress = findViewById(R.id.progress)
        crumbs = findViewById(R.id.crumbs)
        list = findViewById(R.id.fileList)
        titleEmail = findViewById(R.id.titleEmail)
        list.layoutManager = LinearLayoutManager(this)
        list.adapter = adapter

        val server = findViewById<EditText>(R.id.inputServer)
        val email = findViewById<EditText>(R.id.inputEmail)
        val password = findViewById<EditText>(R.id.inputPassword)
        val otp = findViewById<EditText>(R.id.inputOtp)
        val otpLabel = findViewById<TextView>(R.id.otpLabel)

        findViewById<Button>(R.id.btnLogin).setOnClickListener {
            vm.updateServer(server.text.toString())
            vm.updateEmail(email.text.toString())
            vm.updatePassword(password.text.toString())
            vm.updateOtp(otp.text.toString())
            vm.login()
        }
        findViewById<Button>(R.id.btnRefresh).setOnClickListener { vm.loadFiles() }
        findViewById<Button>(R.id.btnUpload).setOnClickListener { picker.launch("*/*") }
        findViewById<Button>(R.id.btnNewFolder).setOnClickListener {
            val input = EditText(this).apply { hint = "Klasör adı" }
            AlertDialog.Builder(this)
                .setTitle("Yeni klasör")
                .setView(input)
                .setPositiveButton("Oluştur") { _, _ ->
                    val name = input.text.toString().trim()
                    if (name.isNotEmpty()) vm.createFolder(name)
                }
                .setNegativeButton("İptal", null)
                .show()
        }
        findViewById<Button>(R.id.btnLogout).setOnClickListener { vm.logout() }
        crumbs.setOnClickListener {
            val state = vm.state.value
            if (state.crumbs.size > 1) vm.goToCrumb(state.crumbs.lastIndex - 1)
        }

        lifecycleScope.launch {
            vm.state.collectLatest { state ->
                progress.visibility = if (state.busy) View.VISIBLE else View.GONE
                if (!state.bootstrapped) return@collectLatest

                if (!state.loggedIn) {
                    loginPanel.visibility = View.VISIBLE
                    filesPanel.visibility = View.GONE
                    server.setText(state.serverUrl)
                    email.setText(state.email)
                    otpLabel.visibility = if (state.requires2FA) View.VISIBLE else View.GONE
                    otp.visibility = if (state.requires2FA) View.VISIBLE else View.GONE
                } else {
                    loginPanel.visibility = View.GONE
                    filesPanel.visibility = View.VISIBLE
                    titleEmail.text = state.user?.email ?: state.email
                    crumbs.text = state.crumbs.joinToString(" / ") { it.name }
                    adapter.submit(state.files)
                }

                state.message?.let {
                    Toast.makeText(this@MainActivity, it, Toast.LENGTH_SHORT).show()
                    vm.clearMessage()
                }
                state.downloaded?.let { file ->
                    openDownloaded(file)
                    vm.consumeDownload()
                }
            }
        }
    }

    private fun openDownloaded(file: File) {
        val uri = FileProvider.getUriForFile(this, "$packageName.files", file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, contentResolver.getType(uri) ?: "*/*")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        runCatching { startActivity(Intent.createChooser(intent, "Dosyayı aç")) }
    }
}

private class FileAdapter(
    private val onOpen: (FileEntry) -> Unit,
    private val onDownload: (FileEntry) -> Unit,
    private val onDelete: (FileEntry) -> Unit,
) : RecyclerView.Adapter<FileAdapter.VH>() {
    private var items: List<FileEntry> = emptyList()

    fun submit(next: List<FileEntry>) {
        items = next
        notifyDataSetChanged()
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
        val view = LayoutInflater.from(parent.context).inflate(R.layout.item_file, parent, false)
        return VH(view)
    }

    override fun getItemCount() = items.size

    override fun onBindViewHolder(holder: VH, position: Int) {
        val item = items[position]
        holder.name.text = item.name
        holder.meta.text = if (item.kind == "folder") "Klasör" else "${item.sizeBytes} bayt"
        holder.itemView.setOnClickListener { onOpen(item) }
        holder.download.visibility = if (item.kind == "file") View.VISIBLE else View.GONE
        holder.download.setOnClickListener { onDownload(item) }
        holder.delete.setOnClickListener { onDelete(item) }
    }

    class VH(view: View) : RecyclerView.ViewHolder(view) {
        val name: TextView = view.findViewById(R.id.itemName)
        val meta: TextView = view.findViewById(R.id.itemMeta)
        val download: Button = view.findViewById(R.id.itemDownload)
        val delete: Button = view.findViewById(R.id.itemDelete)
    }
}
