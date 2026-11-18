# TR Driver 0.13.0 — LocalTel ve izinler

TR Driver 0.13.0 (Android `versionCode` 47) LocalTel'i varsayılan olarak sıfır yapılandırmalı hale getirir. Kullanıcı TR Driver'a giriş yaptıktan sonra uygulama hesabı sunucudan alır; normal arama ekranında SIP sunucusu, realm, port, kullanıcı adı veya parola girilmez. Eski manuel SIP sağlayıcısı yalnızca **LocalTel Aramalar → Arama Durumu → Gelişmiş ayarları göster** altında bulunur.

## Yerleşik çağrı geçidi

- `GET /api/localtel/config` kimliği doğrulanmış cihazın kendi hesabını ilk istekte oluşturur ve WSS adresi, dahili numara, kullanıcı adı, şifreli saklanan hesap parolası ve kısa ömürlü imzalı WebSocket token'ını `Cache-Control: no-store` ile döndürür.
- İstemci WebSocket'i mevcut TR Driver alan adındaki `wss://<alan-adı>/localtel/ws` yoluna açar. Yeni port, NPM host, DNS kaydı veya SIP konteyneri gerekmez. `/localtel/ws` kısa ömürlü bearer token ile doğrulanır; `/api/localtel/ws` eski cihaz-token akışıyla geriye dönük uyumludur.
- Sunucu her kullanıcı için kriptografik rastgele kimlik bilgileri üretir. Üyelikte isteğe bağlı telefon numarası girilmişse normalize edilmiş numara o kullanıcının aranabilir dahili olur; yoksa benzersiz beş haneli dahili atanır. Hesap parolası AES‑256‑GCM ve kullanıcıya bağlı AAD ile `localtel_accounts.password_enc` içinde tutulur; anahtar `LOCAL_TEL_KEY` (yoksa `PERSONAL_BACKUP_KEY`, ardından `SESSION_SECRET`) üzerinden türetilir.
- `invite` hedefi ayrılmış dahili/hesap adıyla çözülür; çağrıya özel, süreli rotada iki katılımcı saklanır. Çağrı sinyali ve G.711 PCMU ses çerçeveleri yalnızca diğer katılımcıya aktarılır. Sunucu, katılımcı olmayan bir kullanıcının çağrı sinyali göndermesini reddeder. Medya WSS üzerinden sunucu tarafından aktarıldığından varsayılan kurulumda ICE/STUN/TURN servisi gerekmez.
- WebSocket token'ları 15 dakika geçerlidir (varsayılan); Android arka plan dinleyicisi token'ı yeniler ve canlı çağrı yokken bağlantıyı tazeler. Kimlik bilgileri ve token'lar sunucu/Android loglarına yazılmaz. Yapılandırma endpoint'i kullanıcı başına hız sınırına ve no-store başlıklarına sahiptir; WSS bağlantıları IP ve kullanıcı başına sınırlanır.
- WebSocket route'u uygulamanın mevcut HTTP(S) portunda çalışır. Ters proxy, aynı TR Driver host için WebSocket upgrade'i uygulamaya iletmelidir.

### GSM gibi çalma (0.16.0)

- Davet, aranan cihaza ulaştıktan sonra **yanıtlanana kadar ~6 sn'de bir yeniden gönderilir**; 45 saniye sonunda çağrı iki taraf için de biter.
- Süre dolunca: arayan cihaz `reject (no_answer)` alır ("Cevapsız: kimse cevap vermedi"), aranan kullanıcıya **cevapsız arama** sinyali gider. Cihaz çevrimdışıysa sinyal, bağlantı kurulduğunda teslim edilir (sunucuda 24 saate kadar kuyrukta tutulur) ve Android "Cevapsız arama" bildirimi gösterir.
- Cihaz çağrın ortasında koparsa da aynı cevapsız kaydı oluşur; `ringing` alan aranan taraf, davet tekrarlarını durdurur (ama süre sayacı işlemeye devam eder).
- **İsteğe bağlı FCM uyanma push'u:** sunucuda `FIREBASE_SERVICE_ACCOUNT` (Firebase service-account JSON) tanımlıysa davet anında cihaza yüksek öncelikli veri push'u gider; uyanan uygulama WebSocket'i yeniden kurar ve davet tekrarını alarak çalar. Push yoksa (ya da Firebase kurulmamışsa) arama yalnızca WebSocket ile çalışır — davranış değişmez.
- Android'in `google-services.json` dosyası `android/app/` altına konmadıkça Firebase hiç başlatılmaz; token yükleme ve push alımı sessizce devre dışı kalır. Dosya eklendiğinde uygulama açılışında (ve WS bağlanınca) token `POST /api/localtel/fcm-token` ile sunucuya kaydedilir.
- Tüm ring/timeout/push loglarında yalnızca anon hash'ler ve sayaçlar bulunur; numara, ad, token ve push gövdesi loglanmaz.

### Yeni ortam değişkenleri

- `LOCAL_TEL_DOMAIN` — gateway URL'sinde kullanılacak alan adı (isteğe bağlı; boşsa `PUBLIC_BASE_URL` hostu, sonra gelen isteğin Host başlığı kullanılır; TLS sertifikasının bu hostu kapsaması gerekir).
- `LOCAL_TEL_TOKEN_TTL` — kısa ömürlü gateway token süresi; varsayılan `15m`.
- `LOCAL_TEL_AUTO_PROVISION` — ilk istekte kullanıcı LocalTel hesabı oluşturma anahtarı; varsayılan `true`.
- `LOCAL_TEL_KEY` — önceden mevcut, isteğe bağlı şifreleme anahtarı; boş bırakılırsa var olan `PERSONAL_BACKUP_KEY`/`SESSION_SECRET` fallback zinciri kullanılır.
- `FIREBASE_SERVICE_ACCOUNT` — isteğe bağlı Firebase service-account JSON (tek satır). Tanımlanırsa gelen aramalar için yüksek öncelikli FCM push'u açılır; boşsa push yok, arama WebSocket üzerinden çalışmaya devam eder.

Ek servis yoktur; LocalTel sinyalizasyon ve ses relay'i TR Driver Go uygulaması içinde çalışır. `docker-compose.stack.yml`, `docker-compose.easy.yml` ve `docker-compose.portainer.yml` aynı değişkenleri uygulama servisine aktarır.

## Android izin sihirbazı ve SMS geri yükleme

İlk açılışta ve ana menü **İzinler** bölümünde Türkçe neden/status gösteren adım adım sihirbaz açılır. Rehber, SMS okuma, telefon/ConnectionService, mikrofon, bildirim, medya/kısmi medya, tüm dosyalar, pil optimizasyonu, tam ekran arama, uygulama içi güncelleme, kamera ve Bluetooth durumlarını gösterir. Kalıcı reddedilen runtime izinleri uygulama bilgisine; özel erişimler kendi Android ayar sayfasına yönlendirilir. Android 13+ kısıtlı SMS izni için `⋮ → Kısıtlı ayarlara izin ver` açıklaması gösterilir. Ana ekranda eksik izin kartı ilgili özelliklerin duraklatıldığını açıklar; yedekleme/arama özellikleri izin kontrolleri olmadan sağlayıcıya erişmez.

SMS yedeklemesi `READ_SMS` ile okunmaya devam eder. Geri yükleme, Android `ROLE_SMS` onayını geçici olarak ister; SMS/WAP push alıcıları, `RESPOND_VIA_MESSAGE` servisi ve `SENDTO` aktivitesi rol uygunluğu için manifestte tanımlıdır. Kayıtlar cihazın SMS sağlayıcısına yinelenen kayıt kontrolüyle eklenir. Tamamlanınca önceki varsayılan SMS uygulamasını seçmek için Android varsayılan uygulamalar ayarları açılır; kullanıcı önceki uygulamayı yeniden seçtiğinde sihirbaz onu açar.

## Yedekleme ve arama davranışı

Kişisel/galeri yedeklemesi Wi‑Fi (ölçülmemiş ağ) ile sınırlı kalır: hız sınırı, artımlı tarama ve arka plan servisi aynıdır. Yedek yalnızca kullanıcı dosyalarını alır (fotoğraf, video, ses, ofis, PDF, zip/rar, exe/apk). `Android/data`, `Android/obb`, gizli klasörler ve önbellek klasörleri yüklenmez; daha önce yüklenmiş kopyalar sunucudan silinmez.

LocalTel bu kısıtı paylaşmaz. Sinyal (WebSocket) ve ses (aynı WebSocket üzerinden G.711 PCMU, sunucu aktarımı) Wi‑Fi **veya** mobil veri ile çalışır. Aktif ağ değişince (Wi‑Fi → mobil) dinleyici yeni ağa bağlanır ve soketi kısa sürede yeniden açar. Uygulama sürecini tek bir ağa kilitlemez; yedek yüklemesi ile arama soketi ayrıdır.

### Mobil veri ve ev yönlendiricisi (0.16.1)

Yerleşik ağ geçidi medyayı ayrı bir UDP/RTP portundan değil, TR Driver'ın kendi WSS bağlantısından taşır. Bu yüzden ICE/STUN/TURN **gerekmez** ve ekstra bir UDP aralık portu açılmaz.

Mobil veriden (ev ağının dışından) arama için telefonun sunucuya ulaşması gerekir:

- TR Driver'ın HTTPS portu (genelde **443/TCP**) yönlendiricide sunucuya iletilmelidir. WebSocket yükseltmesi (`/localtel/ws`) ters vekil tarafından uygulamaya geçirilmelidir.
- `PUBLIC_BASE_URL` veya `LOCAL_TEL_DOMAIN` telefonda çözülen ve sertifikası geçerli olan genel adres olmalıdır. Yalnızca `192.168.x.x` gibi bir iç adres mobil veriden çalışmaz.
- Torrent dinleme portu (varsayılan **42069/TCP ve 42069/UDP**) LocalTel'den bağımsızdır; onu yalnızca torrent eşleri için yönlendirin. Panelde görünen port numarası esas alınır.

Doğrudan SIP (Gelişmiş ayarlar) seçilirse RTP telefonun kendi UDP soketinden gider ve karşı tarafın erişilebilir olması gerekir. Varsayılan TR Driver ağ geçidinde bu yol kullanılmaz.

## API özeti

- `GET /api/localtel/config` — authenticated, kullanıcıya özel, sıfır yapılandırma config + kısa ömürlü gateway token; no-store ve rate-limited.
- `PUT /api/localtel/config`, `DELETE /api/localtel/config` — gelişmiş manuel sağlayıcı yapılandırması.
- `GET /api/localtel/config/provision` — eski manuel cihaz kurulum endpoint'i.
- `POST /api/localtel/signal`, `GET /api/localtel/ws` — eski authenticated signaling endpoint'leri.
- `GET /localtel/ws` — kısa ömürlü bearer token doğrulamalı aynı-domain WSS relay endpoint'i.
