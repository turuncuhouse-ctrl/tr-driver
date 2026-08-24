package net.neciparmagan.trdriver.presentation

import android.app.Application
import android.net.Uri
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.DriveApi
import net.neciparmagan.trdriver.data.FileEntry
import net.neciparmagan.trdriver.data.OfflineCache
import net.neciparmagan.trdriver.data.SessionStore
import net.neciparmagan.trdriver.data.User
import java.io.File

data class Crumb(val id: String?, val name: String)

data class UiState(
    val bootstrapped: Boolean = false,
    val loggedIn: Boolean = false,
    val busy: Boolean = false,
    val message: String? = null,
    val serverUrl: String = "",
    val email: String = "",
    val password: String = "",
    val requires2FA: Boolean = false,
    val challengeToken: String? = null,
    val otp: String = "",
    val displayName: String = "",
    val user: User? = null,
    val crumbs: List<Crumb> = listOf(Crumb(null, "Dosyalarım")),
    val files: List<FileEntry> = emptyList(),
    val downloaded: File? = null,
    val searchQuery: String = "",
    val searching: Boolean = false,
    val offline: Boolean = false,
    val selectionMode: Boolean = false,
    val selectedIds: Set<String> = emptySet(),
    val shareUrl: String? = null,
    /** Manual upload / transfer progress (0–100). */
    val transferPercent: Int = 0,
    val transferLabel: String? = null,
)

class DriveViewModel(app: Application) : AndroidViewModel(app) {
    private val session = SessionStore(app)
    private val api = DriveApi(session, app)
    private val offline = OfflineCache(app)

    private val _state = MutableStateFlow(
        UiState(
            serverUrl = session.serverUrl,
            email = session.email,
            loggedIn = session.isLoggedIn,
        )
    )
    val state: StateFlow<UiState> = _state

    init {
        if (session.isLoggedIn) {
            refreshMeAndList()
        } else {
            _state.update { it.copy(bootstrapped = true) }
        }
    }

    fun updateServer(v: String) = _state.update { it.copy(serverUrl = v) }
    fun updateEmail(v: String) = _state.update { it.copy(email = v) }
    fun updatePassword(v: String) = _state.update { it.copy(password = v) }
    fun updateOtp(v: String) = _state.update { it.copy(otp = v) }
    fun updateDisplayName(v: String) = _state.update { it.copy(displayName = v) }
    fun clearMessage() = _state.update { it.copy(message = null) }
    fun consumeDownload() = _state.update { it.copy(downloaded = null) }
    fun consumeShareUrl() = _state.update { it.copy(shareUrl = null) }

    fun register() {
        val s = _state.value
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null) }
            try {
                session.serverUrl = s.serverUrl
                val name = s.displayName.ifBlank { s.email.substringBefore("@") }
                val resp = api.register(s.email.trim(), s.password, name)
                _state.update {
                    it.copy(
                        busy = false,
                        loggedIn = true,
                        password = "",
                        user = resp.user,
                        crumbs = listOf(Crumb(null, "Dosyalarım")),
                        message = "Hesap oluşturuldu",
                    )
                }
                loadFiles()
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "Kayıt başarısız") }
            }
        }
    }

    fun redeemQr(challengeToken: String, serverUrl: String?) {
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null) }
            try {
                if (!serverUrl.isNullOrBlank()) session.serverUrl = serverUrl.trimEnd('/')
                val resp = api.redeemQr(challengeToken)
                _state.update {
                    it.copy(
                        busy = false,
                        loggedIn = true,
                        serverUrl = session.serverUrl,
                        email = session.email,
                        user = resp.user,
                        crumbs = listOf(Crumb(null, "Dosyalarım")),
                        message = "QR ile giriş başarılı",
                    )
                }
                loadFiles()
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "QR giriş başarısız") }
            }
        }
    }

    fun login() {
        val s = _state.value
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null) }
            try {
                session.serverUrl = s.serverUrl
                val challenge = if (s.requires2FA) s.challengeToken else null
                val code = if (s.requires2FA) s.otp else null
                val resp = api.login(s.email.trim(), s.password, challenge, code)
                if (resp.requires2FA) {
                    _state.update {
                        it.copy(
                            busy = false,
                            requires2FA = true,
                            challengeToken = resp.challengeToken,
                            message = resp.message ?: "E-postanıza gelen kodu girin",
                        )
                    }
                    return@launch
                }
                _state.update {
                    it.copy(
                        busy = false,
                        loggedIn = true,
                        requires2FA = false,
                        challengeToken = null,
                        otp = "",
                        password = "",
                        user = resp.user,
                        crumbs = listOf(Crumb(null, "Dosyalarım")),
                    )
                }
                loadFiles()
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "Giriş başarısız") }
            }
        }
    }

    fun logout() {
        viewModelScope.launch {
            api.logout()
            _state.update {
                UiState(
                    bootstrapped = true,
                    serverUrl = session.serverUrl,
                    email = session.email,
                )
            }
        }
    }

    private fun refreshMeAndList() {
        viewModelScope.launch {
            _state.update { it.copy(busy = true) }
            try {
                val me = api.me()
                _state.update {
                    it.copy(
                        bootstrapped = true,
                        loggedIn = true,
                        busy = false,
                        user = me,
                        crumbs = listOf(Crumb(null, "Dosyalarım")),
                    )
                }
                loadFiles()
            } catch (e: Exception) {
                session.clearAuth()
                _state.update {
                    it.copy(
                        bootstrapped = true,
                        loggedIn = false,
                        busy = false,
                        message = "Oturum sona erdi. Tekrar giriş yapın.",
                    )
                }
            }
        }
    }

    fun loadFiles() {
        val parent = _state.value.crumbs.lastOrNull()?.id
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null, searching = false, searchQuery = "") }
            try {
                val files = api.listFiles(parent)
                    .sortedWith(compareBy({ it.kind != "folder" }, { it.name.lowercase() }))
                offline.saveListing(parent, files)
                _state.update {
                    it.copy(
                        busy = false,
                        files = files,
                        offline = false,
                        selectionMode = false,
                        selectedIds = emptySet(),
                    )
                }
            } catch (e: Exception) {
                val cached = offline.loadListing(parent)
                if (cached != null) {
                    _state.update {
                        it.copy(
                            busy = false,
                            files = cached,
                            offline = true,
                            message = "Çevrimdışı liste (önbellek)",
                        )
                    }
                } else {
                    _state.update { it.copy(busy = false, message = e.message ?: "Liste alınamadı") }
                }
            }
        }
    }

    fun search(query: String) {
        val q = query.trim()
        if (q.isEmpty()) {
            loadFiles()
            return
        }
        viewModelScope.launch {
            _state.update { it.copy(busy = true, searchQuery = q, searching = true, message = null) }
            try {
                val files = api.search(q)
                _state.update {
                    it.copy(
                        busy = false,
                        files = files,
                        offline = false,
                        crumbs = listOf(Crumb(null, "Arama: $q")),
                        selectionMode = false,
                        selectedIds = emptySet(),
                    )
                }
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "Arama başarısız") }
            }
        }
    }

    fun showStarred() {
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null, searching = true) }
            try {
                val files = api.listStarred()
                _state.update {
                    it.copy(
                        busy = false,
                        files = files,
                        crumbs = listOf(Crumb(null, "Yıldızlı")),
                        offline = false,
                    )
                }
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message) }
            }
        }
    }

    fun showRecent() {
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null, searching = true) }
            try {
                val files = api.listRecent()
                _state.update {
                    it.copy(
                        busy = false,
                        files = files,
                        crumbs = listOf(Crumb(null, "Son görüntülenen")),
                        offline = false,
                    )
                }
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message) }
            }
        }
    }

    fun openFolder(entry: FileEntry) {
        if (entry.kind != "folder") return
        val special = _state.value.searching ||
            _state.value.crumbs.any {
                it.name.startsWith("Arama") || it.name == "Yıldızlı" || it.name == "Son görüntülenen"
            }
        _state.update {
            if (special) {
                it.copy(
                    crumbs = listOf(Crumb(null, "Dosyalarım"), Crumb(entry.id, entry.name)),
                    searching = false,
                    selectionMode = false,
                    selectedIds = emptySet(),
                )
            } else {
                it.copy(
                    crumbs = it.crumbs + Crumb(entry.id, entry.name),
                    selectionMode = false,
                    selectedIds = emptySet(),
                )
            }
        }
        loadFiles()
    }

    fun goToCrumb(index: Int) {
        _state.update { it.copy(crumbs = it.crumbs.take(index + 1), searching = false) }
        loadFiles()
    }

    fun createFolder(name: String) {
        val parent = _state.value.crumbs.lastOrNull()?.id
        viewModelScope.launch {
            _state.update { it.copy(busy = true) }
            try {
                api.createFolder(parent, name.trim())
                _state.update { it.copy(busy = false, message = "Klasör oluşturuldu") }
                loadFiles()
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message) }
            }
        }
    }

    fun deleteEntry(entry: FileEntry) {
        viewModelScope.launch {
            _state.update { it.copy(busy = true) }
            try {
                api.delete(entry.id)
                _state.update { it.copy(busy = false, message = "\"${entry.name}\" silindi") }
                loadFiles()
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message) }
            }
        }
    }

    fun upload(uri: Uri) {
        val parent = _state.value.crumbs.lastOrNull()?.id
        viewModelScope.launch {
            _state.update {
                it.copy(
                    busy = true,
                    message = null,
                    transferPercent = 0,
                    transferLabel = "Yükleniyor…",
                )
            }
            try {
                api.upload(parent, uri) { sent, total ->
                    val pct = if (total > 0) ((sent * 100) / total).toInt().coerceIn(0, 100) else 0
                    val label = "Yükleniyor · ${SessionStore.formatBytes(sent)} / ${SessionStore.formatBytes(total)} · %$pct"
                    _state.update { it.copy(transferPercent = pct, transferLabel = label, busy = true) }
                }
                _state.update {
                    it.copy(
                        busy = false,
                        message = "Yükleme tamam",
                        transferPercent = 0,
                        transferLabel = null,
                    )
                }
                loadFiles()
            } catch (e: Exception) {
                _state.update {
                    it.copy(
                        busy = false,
                        message = e.message ?: "Yükleme başarısız",
                        transferPercent = 0,
                        transferLabel = null,
                    )
                }
            }
        }
    }

    fun download(entry: FileEntry) {
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = "İndiriliyor…") }
            try {
                val file = api.downloadToCache(entry)
                _state.update { it.copy(busy = false, message = "İndirildi: ${file.name}", downloaded = file) }
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "İndirme başarısız") }
            }
        }
    }

    fun enterSelection(entryId: String) {
        _state.update {
            it.copy(selectionMode = true, selectedIds = setOf(entryId))
        }
    }

    fun toggleSelection(entryId: String) {
        _state.update {
            if (!it.selectionMode) return@update it
            val next = it.selectedIds.toMutableSet()
            if (!next.add(entryId)) next.remove(entryId)
            it.copy(selectedIds = next, selectionMode = next.isNotEmpty())
        }
    }

    fun clearSelection() = _state.update { it.copy(selectionMode = false, selectedIds = emptySet()) }

    fun deleteSelected() {
        val ids = _state.value.selectedIds.toList()
        if (ids.isEmpty()) return
        viewModelScope.launch {
            _state.update { it.copy(busy = true) }
            var ok = 0
            for (id in ids) {
                runCatching { api.delete(id); ok++ }
            }
            _state.update {
                it.copy(
                    busy = false,
                    selectionMode = false,
                    selectedIds = emptySet(),
                    message = "$ok öğe silindi",
                )
            }
            loadFiles()
        }
    }

    fun downloadSelected() {
        val selected = _state.value.files.filter { it.id in _state.value.selectedIds && it.kind == "file" }
        if (selected.isEmpty()) {
            _state.update { it.copy(message = "İndirilecek dosya seçin") }
            return
        }
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = "Toplu indirme…") }
            var last: File? = null
            var ok = 0
            for (entry in selected) {
                runCatching {
                    last = api.downloadToCache(entry)
                    ok++
                }
            }
            _state.update {
                it.copy(
                    busy = false,
                    selectionMode = false,
                    selectedIds = emptySet(),
                    downloaded = last,
                    message = "$ok dosya indirildi (çevrimdışı klasör)",
                )
            }
        }
    }

    fun shareEntry(entry: FileEntry) {
        viewModelScope.launch {
            _state.update { it.copy(busy = true, message = null) }
            try {
                val res = api.createShareLink(entry.id)
                val full = session.serverUrl.trimEnd('/') + (if (res.url.startsWith("/")) res.url else "/s/${res.token}")
                _state.update { it.copy(busy = false, shareUrl = full, message = "Paylaşım linki hazır") }
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "Paylaşım oluşturulamadı") }
            }
        }
    }

    fun toggleStar(entry: FileEntry) {
        viewModelScope.launch {
            try {
                api.setStarred(entry.id, !entry.starred)
                _state.update { st ->
                    st.copy(
                        files = st.files.map {
                            if (it.id == entry.id) it.copy(starred = !entry.starred) else it
                        },
                        message = if (!entry.starred) "Yıldızlandı" else "Yıldız kaldırıldı",
                    )
                }
            } catch (e: Exception) {
                _state.update { it.copy(message = e.message) }
            }
        }
    }

    fun listOfflineDownloads(): List<File> = offline.listDownloaded()
}
