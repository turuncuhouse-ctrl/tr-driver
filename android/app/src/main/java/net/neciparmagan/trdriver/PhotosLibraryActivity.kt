package net.neciparmagan.trdriver

import android.content.Intent
import android.graphics.Color
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.ImageView
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.GridLayoutManager
import androidx.recyclerview.widget.RecyclerView
import coil.load
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.Headers

/**
 * Google Photos-like browser for TR Photos on the server.
 * Works after free-up: originals may be gone locally; thumbs load from cloud.
 */
class PhotosLibraryActivity : AppCompatActivity() {
    private lateinit var session: SessionStore
    private lateinit var api: DriveApi
    private lateinit var title: TextView
    private lateinit var crumbs: TextView
    private lateinit var progress: ProgressBar
    private lateinit var grid: RecyclerView

    private data class Crumb(val id: String, val name: String)

    private val stack = ArrayList<Crumb>()
    private val adapter = PhotosAdapter(
        onOpen = { openEntry(it) },
    )

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_photos_library)
        session = SessionStore(this)
        api = DriveApi(session, this)

        if (!session.isLoggedIn) {
            Toast.makeText(this, "Önce giriş yapın", Toast.LENGTH_LONG).show()
            finish()
            return
        }

        title = findViewById(R.id.photosTitle)
        crumbs = findViewById(R.id.photosCrumbs)
        progress = findViewById(R.id.photosProgress)
        grid = findViewById(R.id.photosGrid)
        grid.layoutManager = GridLayoutManager(this, 3)
        grid.adapter = adapter
        adapter.updateAuth(session.token.orEmpty(), session.serverUrl)

        findViewById<Button>(R.id.btnPhotosUp).setOnClickListener { goUp() }
        findViewById<Button>(R.id.btnPhotosClose).setOnClickListener { finish() }

        lifecycleScope.launch { openRoot() }
    }

    private suspend fun openRoot() {
        progress.visibility = View.VISIBLE
        try {
            val rootId = api.ensurePhotosRoot()
            stack.clear()
            stack += Crumb(rootId, "TR Photos")
            loadCurrent()
        } catch (e: Exception) {
            Toast.makeText(this, e.message ?: "TR Photos açılamadı", Toast.LENGTH_LONG).show()
        } finally {
            progress.visibility = View.GONE
        }
    }

    private fun loadCurrent() {
        val current = stack.lastOrNull() ?: return
        progress.visibility = View.VISIBLE
        crumbs.text = stack.joinToString(" / ") { it.name }
        title.text = current.name
        lifecycleScope.launch {
            try {
                val files = api.listFiles(current.id)
                adapter.submit(files)
            } catch (e: Exception) {
                Toast.makeText(this@PhotosLibraryActivity, e.message, Toast.LENGTH_LONG).show()
            } finally {
                progress.visibility = View.GONE
            }
        }
    }

    private fun openEntry(entry: FileEntry) {
        if (entry.kind == "folder") {
            stack += Crumb(entry.id, entry.name)
            loadCurrent()
            return
        }
        val mime = MainActivity.resolveMime(entry)
        if (MainActivity.isPreviewableMedia(mime) || mime == "application/pdf") {
            startActivity(
                Intent(this, MediaPreviewActivity::class.java).apply {
                    putExtra(MediaPreviewActivity.EXTRA_ID, entry.id)
                    putExtra(MediaPreviewActivity.EXTRA_NAME, entry.name)
                    putExtra(MediaPreviewActivity.EXTRA_MIME, mime)
                },
            )
        } else {
            Toast.makeText(this, "Bu tür önizlenemiyor", Toast.LENGTH_SHORT).show()
        }
    }

    private fun goUp() {
        if (stack.size <= 1) {
            finish()
            return
        }
        stack.removeAt(stack.lastIndex)
        loadCurrent()
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        if (stack.size > 1) {
            goUp()
        } else {
            super.onBackPressed()
        }
    }

    private class PhotosAdapter(
        private val onOpen: (FileEntry) -> Unit,
    ) : RecyclerView.Adapter<PhotosAdapter.VH>() {
        private var items: List<FileEntry> = emptyList()
        private var token: String = ""
        private var serverUrl: String = ""

        fun updateAuth(token: String, serverUrl: String) {
            this.token = token
            this.serverUrl = serverUrl.trimEnd('/')
        }

        fun submit(next: List<FileEntry>) {
            items = next
            notifyDataSetChanged()
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH {
            val view = LayoutInflater.from(parent.context).inflate(R.layout.item_photo_grid, parent, false)
            return VH(view)
        }

        override fun getItemCount(): Int = items.size

        override fun onBindViewHolder(holder: VH, position: Int) {
            val item = items[position]
            holder.name.text = item.name
            holder.thumb.setImageDrawable(null)
            holder.itemView.setOnClickListener { onOpen(item) }
            when {
                item.kind == "folder" -> {
                    holder.thumb.setBackgroundColor(Color.parseColor("#0B5CAD"))
                }
                else -> {
                    holder.thumb.setBackgroundColor(Color.parseColor("#E8F2FC"))
                    val mime = MainActivity.resolveMime(item)
                    if (mime.startsWith("image/") && token.isNotBlank() && serverUrl.isNotBlank()) {
                        val url = "$serverUrl/api/files/download/${item.id}?inline=1"
                        holder.thumb.load(url) {
                            headers(Headers.headersOf("Authorization", "Bearer $token"))
                            size(320, 320)
                            crossfade(true)
                        }
                    } else if (mime.startsWith("video/")) {
                        holder.thumb.setBackgroundColor(Color.parseColor("#1F2937"))
                    }
                }
            }
        }

        class VH(view: View) : RecyclerView.ViewHolder(view) {
            val thumb: ImageView = view.findViewById(R.id.photoThumb)
            val name: TextView = view.findViewById(R.id.photoName)
        }
    }
}
