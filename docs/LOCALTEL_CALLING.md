# TR Driver — Aşama 3: LocalTel IP Sesli Arama

Bu belge TR Driver **0.12.0** sürümünde eklenen uygulama içi WhatsApp benzeri **LocalTel** sesli IP arama altyapısını, mimari kararlarını, bilinmeyen/varsayılan noktaları ve güvenlik sınırlarını açıklar.

## 1. Keşif ve Bilinmeyenler / Varsayımlar

Depo, önceki PR'lar (#1–#6), dokümantasyon ve kamuya açık kaynaklar incelendiğinde LocalTel için sabit bir satıcı API'si veya örnek SIP sunucusu yer almıyordu. Bu nedenle iki standart aktarım modunu destekleyen yapılandırılabilir bir sağlayıcı arayüzü (`LocalTelCallProvider`) kuruldu:

1. **Doğrudan SIP Modu (`providerType = "sip"`, `SipLocalTelProvider`)**
   - Varsayım: Mevcut LocalTel altyapısı standart **RFC 3261 SIP** (`REGISTER` / `INVITE` / `ACK` / `CANCEL` / `BYE`), Digest (`MD5` veya `SHA-256`) kimlik doğrulama ve **SDP + G.711 PCMU/PCMA RTP (RFC 3550 / RFC 2833 DTMF)** medya akışını destekler.
   - Desteklenen taşımalar: `udp` (varsayılan port `5060`), `tcp` (`5060`), `tls` (`5061`).
   - Bilinmeyen / dış ağ sınırı: Cihaz sıkı bir mobil taşıyıcı NAT arkasındaysa ve PBX tarafında simetrik RTP / ICE-STUN etkin değilse doğrudan UDP/SIP ulaşamayabilir; bu senaryolar için aşağıdaki Ağ Geçidi (Gateway) modu sağlanmıştır.
2. **Ağ Geçidi / Relay Modu (`providerType = "gateway"`, `GatewayLocalTelProvider`)**
   - Varsayım: Sunucu URL'si boş bırakılırsa uygulama kimliği doğrulanmış TR Driver sunucusundaki `/api/localtel/ws` WebSocket aktarıcısına bağlanır (veya harici bir `wss://` / `https://` LocalTel ağ geçidine bağlanabilir).
   - Çağrı sinyalleri (`invite`, `ringing`, `accept`, `reject`, `hangup`, `cancel`) ve G.711 PCMU ses çerçeveleri JSON WebSocket mesajlarıyla taşınır.
3. **Arka Plan Uyanma (Push)**
   - TR Driver de-Googled / kendi kendine barındırılan cihazları da desteklediği için zorunlu FCM bağımlılığı eklenmemiştir. Gelen aramalar `LocalTelListenerService` (`dataSync` ön plan servisi) açıkken canlı SIP/WebSocket oturumu üzerinden alınır. İleride özel push ağ geçidi eklenirse `LocalTelCallProvider` arayüzü değişmeden uyarlanabilir.

## 2. Android Mimarisi

`android/app/src/main/java/net/neciparmagan/trdriver/calls/`

- **`LocalTelCallProvider` (`LocalTelModels.kt`)**: Sağlayıcıdan bağımsız çağrı başlatma, yanıtlama, reddetme, kapatma, sessize alma ve DTMF arayüzü.
- **`PhoneNumberNormalizer` & `ContactMatcher` & `AndroidContactDirectory`**:
  - Aşama 2 `READ_CONTACTS` iznini kullanarak Android rehberini (`ContactsContract`) yerel dizine alır (hiçbir kişi bilgisi günlüklere yazılmaz).
  - Türkiye (`+90`, `0090`, `05xx`, `5xx`, sabit hatlar, `444` numaralar), uluslararası E.164 (`+1`, `+49` vb. — farklı ülke kodları arasında yanlış eşleşme engellenir), kısa dahili (`2–6` hane) ve `sip:`/`tel:` URI biçimlerini tutarlı biçimde normalleştirir.
- **`CallStateMachine`**: Saf Kotlin durum makinesi (`IDLE → OUTGOING_DIALING → OUTGOING_RINGING → INCOMING_RINGING → CONNECTING → ACTIVE → ENDED`). Çağrı bitiminde sessiz/hoparlör/bekletme bayraklarını sıfırlar ve temizlik gereksinimini üretir.
- **Kilit Ekranı ve Telecom Entegrasyonu**:
  - `TrConnectionService` & `TelecomHelper`: `PhoneAccount.CAPABILITY_SELF_MANAGED` ile sisteme VoIP araması bildirir (`tel` / `sip` şemaları), cihaz desteklemiyorsa sorunsuz doğrudan çağrı akışına düşer.
  - `TrCallService`: `phoneCall|microphone` ön plan servisi; zil çalarken yalnızca `PHONE_CALL` tipiyle başlar, `MICROPHONE` tipini yalnızca çağrı yanıtlanıp `RECORD_AUDIO` izni doğrulandıktan sonra ekler (Android 14 uyumlu).
  - `CallNotifications`: `NotificationCompat.CallStyle.forIncomingCall` + `setFullScreenIntent(..., true)` ile kilit ekranında yüksek öncelikli çağrı bildirimi üretir.
  - `CallActivity`: `showWhenLocked` + `turnScreenOn` ve sabit konumlu alt kontrol yuvası sayesinde durum değişiminde kaymayan büyük **Aç** / **Reddet** düğmeleri sunar.
  - `CallAudioController`: `AudioFocusRequest`, zil sesi, giden çalma tonu, `MusicService` duraklatma, hoparlör/Bluetooth ses yönlendirme, yakınlık sensörü kilidi ve çağrı bitiminde tam kaynak serbest bırakma.
- **İzin Akışı (`CallPermissions`)**: `RECORD_AUDIO`, `READ_CONTACTS`, `POST_NOTIFICATIONS` (Android 13+) ve `USE_FULL_SCREEN_INTENT` (Android 14+ Ayarlar yönlendirmesi) her zaman önce Türkçe açıklama diyaloğu gösterilerek istenir.

## 3. Sunucu API'si (`internal/localtel`)

Tüm uç noktalar `authHandler.RequireAuth` arkasındadır ve yalnızca çağıranın kendi kullanıcı kimliğine (`user_id`) bağlıdır:

- `GET /api/localtel/config` — Kayıtlı ayar özetini döner (`hasSecret: bool`, ham parola **asla** dönülmez).
- `PUT /api/localtel/config` — Ayarları ve parolayı doğrular, `LOCAL_TEL_KEY` (veya `PERSONAL_BACKUP_KEY` / `SESSION_SECRET`) üzerinden kullanıcıya bağlı **AES‑256‑GCM** ile `localtel_configs` tablosuna şifreli yazar. Parola alanı boş gönderilirse önceki parola korunur (`clearSecret: true` ile silinir).
- `DELETE /api/localtel/config` — Kullanıcının LocalTel yapılandırmasını siler.
- `GET /api/localtel/config/provision` — Kimliği doğrulanmış cihaza `Cache-Control: no-store` başlığıyla cihaz kurulum bilgisini verir.
- `POST /api/localtel/signal` ve `GET /api/localtel/ws` — Yalnızca aynı kullanıcının bağlı cihazları arasında çalışan WebSocket/HTTP çağrı sinyalizasyon aktarıcısı (`Origin` doğrulamalı, hız sınırlı).

## 4. Mevcut Yedekleme Davranışının Korunması

- `TrKeepAliveService` ve `GalleryBackupWorker` yalnızca Wi‑Fi üzerinde, hız/bant genişliği limitleriyle çalışmaya devam eder (`wifiOnlyBackup = true` korunur).
- LocalTel aramaları ayrı servisler (`TrCallService`, `LocalTelListenerService`) üzerinden hem Wi‑Fi hem mobil veri ağlarında yedeklemeyi etkilemeden çalışır.
