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
)

class DriveViewModel(app: Application) : AndroidViewModel(app) {
    private val session = SessionStore(app)
    private val api = DriveApi(session, app)

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
            _state.update { it.copy(busy = true, message = null) }
            try {
                val files = api.listFiles(parent)
                    .sortedWith(compareBy({ it.kind != "folder" }, { it.name.lowercase() }))
                _state.update { it.copy(busy = false, files = files) }
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "Liste alınamadı") }
            }
        }
    }

    fun openFolder(entry: FileEntry) {
        if (entry.kind != "folder") return
        _state.update { it.copy(crumbs = it.crumbs + Crumb(entry.id, entry.name)) }
        loadFiles()
    }

    fun goToCrumb(index: Int) {
        _state.update { it.copy(crumbs = it.crumbs.take(index + 1)) }
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
            _state.update { it.copy(busy = true, message = "Yükleniyor…") }
            try {
                api.upload(parent, uri)
                _state.update { it.copy(busy = false, message = "Yükleme tamam") }
                loadFiles()
            } catch (e: Exception) {
                _state.update { it.copy(busy = false, message = e.message ?: "Yükleme başarısız") }
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
}
