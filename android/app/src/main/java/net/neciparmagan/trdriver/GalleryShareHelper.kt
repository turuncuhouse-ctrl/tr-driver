package net.neciparmagan.trdriver

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.print.PrintAttributes
import android.print.PrintManager
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.core.content.FileProvider
import net.neciparmagan.trdriver.data.LocalMedia
import java.io.File

/**
 * Google Photos / sistem galeri tarzı paylaşım seçenekleri (TR markası korunur).
 */
object GalleryShareHelper {

    fun showLocalShareSheet(activity: Activity, items: List<LocalMedia>) {
        if (items.isEmpty()) {
            Toast.makeText(activity, "Paylaşılacak öğe yok", Toast.LENGTH_SHORT).show()
            return
        }
        val labels = mutableListOf(
            "Uygulamalarla paylaş",
            "Kopyala / panoya URI",
            "Birlikte aç…",
        )
        if (items.size == 1 && !items.first().isVideo) {
            labels += "Yazdır"
        }
        AlertDialog.Builder(activity)
            .setTitle(if (items.size == 1) "Paylaş · ${items.first().displayName}" else "Paylaş · ${items.size} öğe")
            .setItems(labels.toTypedArray()) { _, which ->
                when (labels[which]) {
                    "Uygulamalarla paylaş" -> shareStreams(activity, items)
                    "Kopyala / panoya URI" -> copyUris(activity, items)
                    "Birlikte aç…" -> openWith(activity, items.first())
                    "Yazdır" -> printImage(activity, items.first())
                }
            }
            .setNegativeButton("İptal", null)
            .show()
    }

    fun shareStreams(activity: Activity, items: List<LocalMedia>) {
        if (items.isEmpty()) return
        if (items.size == 1) {
            val item = items.first()
            val send = Intent(Intent.ACTION_SEND).apply {
                type = item.mimeType.ifBlank { "*/*" }
                putExtra(Intent.EXTRA_STREAM, item.uri)
                putExtra(Intent.EXTRA_SUBJECT, item.displayName)
                clipData = ClipData.newUri(activity.contentResolver, item.displayName, item.uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            activity.startActivity(Intent.createChooser(send, "Paylaş"))
            return
        }
        val uris = ArrayList(items.map { it.uri })
        val send = Intent(Intent.ACTION_SEND_MULTIPLE).apply {
            type = commonMime(items)
            putParcelableArrayListExtra(Intent.EXTRA_STREAM, uris)
            clipData = ClipData.newUri(activity.contentResolver, "media", uris.first()).also { clip ->
                for (i in 1 until uris.size) {
                    clip.addItem(ClipData.Item(uris[i]))
                }
            }
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        activity.startActivity(Intent.createChooser(send, "Paylaş (${items.size})"))
    }

    fun shareFile(activity: Activity, file: File, displayName: String, mime: String) {
        val uri = try {
            FileProvider.getUriForFile(activity, "${activity.packageName}.files", file)
        } catch (e: Exception) {
            Toast.makeText(activity, e.message ?: "Paylaşım başarısız", Toast.LENGTH_LONG).show()
            return
        }
        val send = Intent(Intent.ACTION_SEND).apply {
            type = mime.ifBlank { "*/*" }
            putExtra(Intent.EXTRA_STREAM, uri)
            putExtra(Intent.EXTRA_SUBJECT, displayName)
            clipData = ClipData.newUri(activity.contentResolver, displayName, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        activity.startActivity(Intent.createChooser(send, "Paylaş"))
    }

    fun shareTextLink(activity: Activity, url: String, title: String = "Bağlantı") {
        val send = Intent(Intent.ACTION_SEND).apply {
            type = "text/plain"
            putExtra(Intent.EXTRA_TEXT, url)
            putExtra(Intent.EXTRA_SUBJECT, title)
        }
        activity.startActivity(Intent.createChooser(send, "Bağlantıyı paylaş"))
    }

    fun copyText(context: Context, text: String, toast: String = "Kopyalandı") {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText("tr-galeri", text))
        Toast.makeText(context, toast, Toast.LENGTH_SHORT).show()
    }

    private fun copyUris(activity: Activity, items: List<LocalMedia>) {
        val text = items.joinToString("\n") { it.uri.toString() }
        copyText(activity, text, if (items.size == 1) "URI kopyalandı" else "${items.size} URI kopyalandı")
    }

    private fun openWith(activity: Activity, item: LocalMedia) {
        val view = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(item.uri, item.mimeType.ifBlank { "*/*" })
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        activity.startActivity(Intent.createChooser(view, "Birlikte aç"))
    }

    private fun printImage(activity: Activity, item: LocalMedia) {
        try {
            val web = WebView(activity)
            web.webViewClient = object : WebViewClient() {
                override fun onPageFinished(view: WebView?, url: String?) {
                    val pm = activity.getSystemService(Context.PRINT_SERVICE) as PrintManager
                    val adapter = web.createPrintDocumentAdapter("TR_Galeri_${item.displayName}")
                    pm.print("TR Galeri", adapter, PrintAttributes.Builder().build())
                }
            }
            web.loadUrl(item.uri.toString())
        } catch (e: Exception) {
            Toast.makeText(activity, "Yazdırma desteklenmiyor: ${e.message}", Toast.LENGTH_LONG).show()
            shareStreams(activity, listOf(item))
        }
    }

    private fun commonMime(items: List<LocalMedia>): String {
        val types = items.map { it.mimeType.substringBefore('/') }.distinct()
        return when {
            types.size == 1 && types.first() == "image" -> "image/*"
            types.size == 1 && types.first() == "video" -> "video/*"
            else -> "*/*"
        }
    }
}
