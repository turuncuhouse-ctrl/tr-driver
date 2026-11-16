package net.neciparmagan.trdriver.calls

import android.content.Context
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging
import com.google.android.gms.tasks.Tasks
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import net.neciparmagan.trdriver.data.SessionStore
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * Uploads this device's FCM token to `POST /api/localtel/fcm-token` so the
 * server can send a high-priority wake push when a call is invited.
 *
 * Entirely optional: the feature only exists in builds that ship a
 * `google-services.json` (Firebase is then initialized by the Google Services
 * plugin). Without it FirebaseApp is empty and every call here is a no-op —
 * calling keeps working over the plain WebSocket.
 *
 * Tokens are credentials and are never written to logs.
 */
object FcmRegistrar {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val lastUploaded = AtomicReference("")
    private val http = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    fun register(context: Context) {
        val appContext = context.applicationContext
        if (FirebaseApp.getApps(appContext).isEmpty()) return
        val session = SessionStore(appContext)
        if (session.serverUrl.isBlank() || session.token.isNullOrBlank()) return
        scope.launch {
            val token = runCatching {
                Tasks.await(FirebaseMessaging.getInstance().token)
            }.getOrNull()
            upload(session, token)
        }
    }

    /** Called from [TrFcmMessagingService.onNewToken] after a token rotation. */
    fun uploadNewToken(context: Context, token: String?) {
        if (token.isNullOrBlank()) return
        val appContext = context.applicationContext
        if (FirebaseApp.getApps(appContext).isEmpty()) return
        val session = SessionStore(appContext)
        if (session.serverUrl.isBlank() || session.token.isNullOrBlank()) return
        scope.launch { upload(session, token) }
    }

    private fun upload(session: SessionStore, token: String?) {
        if (token.isNullOrBlank() || token == lastUploaded.get()) return
        val payload = JSONObject().put("token", token).toString()
        val request = Request.Builder()
            .url("${session.serverUrl.trimEnd('/')}/api/localtel/fcm-token")
            .header("Authorization", "Bearer ${session.token.orEmpty()}")
            .post(payload.toRequestBody("application/json".toMediaType()))
            .build()
        runCatching {
            http.newCall(request).execute().use { response ->
                if (response.isSuccessful) {
                    lastUploaded.set(token)
                }
            }
        }
        // Failures stay silent: pushes are optional and the token itself must
        // never appear in a log line.
    }
}
