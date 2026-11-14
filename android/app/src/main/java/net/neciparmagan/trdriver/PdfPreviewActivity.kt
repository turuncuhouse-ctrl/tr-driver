package net.neciparmagan.trdriver

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.pdf.PdfRenderer
import android.os.Bundle
import android.os.ParcelFileDescriptor
import android.view.LayoutInflater
import android.view.ViewGroup
import android.widget.Button
import android.widget.ImageView
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.recyclerview.widget.RecyclerView
import androidx.viewpager2.widget.ViewPager2
import java.io.File

/**
 * In-app read-only PDF preview (no editing).
 *
 * Pages render on demand with [PdfRenderer] and swipe via ViewPager2.
 * Everything runs on the UI thread: a single PdfRenderer instance must not
 * be shared across threads, and ViewPager2 binds on the UI thread.
 */
class PdfPreviewActivity : AppCompatActivity() {
    private var renderer: PdfRenderer? = null
    private var fd: ParcelFileDescriptor? = null
    private var pageCount = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_pdf_preview)
        val file = File(intent.getStringExtra(EXTRA_PATH).orEmpty())
        if (!file.exists() || file.length() == 0L) {
            Toast.makeText(this, "PDF bulunamadı", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        findViewById<TextView>(R.id.pdfTitle).text = file.name
        try {
            fd = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
            renderer = PdfRenderer(fd!!)
            pageCount = renderer!!.pageCount
        } catch (e: Exception) {
            Toast.makeText(this, "PDF açılamadı: ${e.message}", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        if (pageCount <= 0) {
            Toast.makeText(this, "PDF boş", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        val pager = findViewById<ViewPager2>(R.id.pdfPager)
        val counter = findViewById<TextView>(R.id.pdfCounter)
        pager.adapter = PdfPageAdapter(pageCount) { position -> renderPage(position) }
        pager.registerOnPageChangeCallback(
            object : ViewPager2.OnPageChangeCallback() {
                override fun onPageSelected(position: Int) {
                    counter.text = "${position + 1} / $pageCount"
                }
            },
        )
        counter.text = "1 / $pageCount"
        findViewById<Button>(R.id.btnPdfPrev).setOnClickListener {
            pager.currentItem = (pager.currentItem - 1).coerceAtLeast(0)
        }
        findViewById<Button>(R.id.btnPdfNext).setOnClickListener {
            pager.currentItem = (pager.currentItem + 1).coerceAtMost(pageCount - 1)
        }
        findViewById<Button>(R.id.btnClosePdf).setOnClickListener { finish() }
    }

    private fun renderPage(position: Int): Bitmap? {
        val r = renderer ?: return null
        if (position < 0 || position >= pageCount) return null
        return try {
            r.openPage(position).use { page ->
                val screenW = resources.displayMetrics.widthPixels.coerceAtLeast(1)
                val scale = (screenW.toFloat() / page.width.toFloat()).coerceIn(1f, 3f)
                val bmp = Bitmap.createBitmap(
                    (page.width * scale).toInt().coerceAtLeast(1),
                    (page.height * scale).toInt().coerceAtLeast(1),
                    Bitmap.Config.ARGB_8888,
                )
                // White background: transparent PDFs render black otherwise.
                bmp.eraseColor(Color.WHITE)
                page.render(bmp, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                bmp
            }
        } catch (_: Exception) {
            null
        }
    }

    override fun onDestroy() {
        runCatching { renderer?.close() }
        runCatching { fd?.close() }
        renderer = null
        fd = null
        super.onDestroy()
    }

    private class PdfPageAdapter(
        private val count: Int,
        private val render: (Int) -> Bitmap?,
    ) : RecyclerView.Adapter<PdfPageAdapter.PdfVH>() {
        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): PdfVH {
            val view = LayoutInflater.from(parent.context)
                .inflate(R.layout.item_pdf_page, parent, false)
            return PdfVH(view)
        }

        override fun getItemCount(): Int = count

        override fun onBindViewHolder(holder: PdfVH, position: Int) {
            val bmp = render(position)
            if (bmp != null) {
                holder.image.setImageBitmap(bmp)
            } else {
                holder.image.setImageDrawable(null)
            }
        }

        class PdfVH(view: android.view.View) : RecyclerView.ViewHolder(view) {
            val image: ImageView = view.findViewById(R.id.pdfPageImage)
        }
    }

    companion object {
        private const val EXTRA_PATH = "pdf_path"

        fun intent(context: Context, absolutePath: String): Intent =
            Intent(context, PdfPreviewActivity::class.java).putExtra(EXTRA_PATH, absolutePath)
    }
}
