# Changelog

## 0.11.0 — Rehber ve SMS yedeği (şifreli, Wi‑Fi, artımlı)

### Added
- Android rehber yedeği (`READ_CONTACTS`): ad, telefon, e‑posta, adres, kurum, not, takma ad, doğum günü, web sitesi ve yıldız bilgisi
- Android SMS yedeği (`READ_SMS`): numara, mesaj metni, tarih, tür, okundu bilgisi ve thread kimliği
- İzinler her zaman açıklama diyaloğuyla istenir (ne okunuyor, neden, nerede saklanıyor, nasıl kapatılır); ayarlardan rehber ve SMS anahtarları ayrı ayrı açılıp kapatılabilir
- Sunucu API'si: `GET /api/personal-backup/status`, `GET|POST|DELETE /api/personal-backup/{contacts,sms}` (artımlı senkron, geri yükleme listesi, tek tıkla silme)
- Kişisel veriler sunucuda AES‑256‑GCM ile şifreli saklanır; satır anahtarı HMAC tabanlı kör indekstir, şifre çözme kullanıcı + tür bağlıdır
- Android ayarlarına "Sunucudaki rehber/SMS yedeğini sil" düğmesi ve tür başına son eşitleme durumu

### Changed
- Yedek türleri artık rehber ve SMS'i de kapsar; arka plan servisi ve WorkManager yedeği aynı Wi‑Fi penceresinde medya → rehber → SMS sırasıyla çalışır
- Rehber/SMS gönderimi küçük partiler hâlinde ve aralarda bekleme ile yapılır; mevcut yükleme hız sınırı ve Wi‑Fi zorunluluğu aynen korunur
- Artımlı çalışma: değişmeyen kayıtlar içerik hash'i ile atlanır, silinenler yalnızca tam tarama sonrası bildirilir
- Sunucu kopyası eksikse (başka cihazdan silinmiş ya da geri yüklenmiş veritabanı) uygulama bunu altı saatte bir fark eder ve kayıtları kendiliğinden yeniden yükler

### Security
- Kişisel veri (PII) günlüklere yazılmaz; API hataları istek gövdesini yansıtmaz
- Tüm uç noktalar oturum/cihaz token'ı ister ve yalnızca çağıranın kendi kayıtlarını döndürür; yazma uçlarında kullanıcı başına hız sınırı vardır
- Gövde boyutu ve kayıt boyutu sunucu tarafında sınırlandırılır (4 MiB istek, 16 KiB kişi, 64 KiB SMS)
- `PERSONAL_BACKUP_KEY` ile şifreleme anahtarı `SESSION_SECRET`'ten ayrılabilir; boşsa oturum sırrı türetilir

## 0.10.0 — Tüm dosya türü yedeği, artımlı yükleme ve güçlü güncelleme

### Added
- Yerel tür anahtarları (fotoğraf, video, ses, dosya) için ayrı açma/kapatma anahtarları
- Hızlı klasör seçimi (İndir, DCIM, Pictures, Documents, Music, Movies, WhatsApp) ve kullanıcı klasörü seçimi
- Manuel "Güncelle" düğmesi; imza sertifikası doğrulaması artı APK SHA-256
- Artımlı yedekleme ve devam ettirilebilir (parçalı) yükleme API'si
- Yükleme başına dosya boyutu sınırı ve yollar için geçersizlik doğrulaması
- Tüm dosya erişimi (MANAGE_EXTERNAL_STORAGE) isteği ve açıklama diyaloğu

### Changed
- Güncelleme kontrolü oturum açmadan da çalışır; arka plan indirmeleri yalnız Wi-Fi, manuel indirme kullanıcı onayıyla mobil veriye izin verir
- Dosya taramaları tüm uzantıları kapsar (pdf, doc, xls, zip, apk vb.)
- Yedekleme yalnızca yeni/değişen dosyaları yükler (artımlı)

### Fixed
- Büyük dosyaların ağ kesintisinde kaldığı yerden devam etmemesi
- Aynı dosyanın birden fazla kaynaktan iki kez yüklenmesi
- İzin verilmeyen ses/dosya kaynaklarının gereksiz yeniden taranması


## 0.9.0 — Drive benzeri arayüz + sunucu yedekleme

### Added
- Android çöp kutusu: liste, tekli/toplu geri yükleme ve kalıcı silme (üst klasör uyarısıyla)
- Android hesap kartında depolama göstergesi (kullanılan/kota/yüzde)
- Android uygulama-içi salt-okunur PDF önizleme (kaydırmalı sayfalar)
- Yedek durumu ekranında bekleyen dosya sayısı + son tam yedek zamanı
- Her gece otomatik şifreli sunucu yedeği: `pg_dump -Fc` + `DATA_DIR`, artımlı `restic` snapshot'ları
- S3 offsite replikasyonu (Wasabi/B2/Hetzner/MinIO/AWS uyumlu) + kısmi-başarı takibi
- Saklama politikası (keep-daily/weekly/monthly + prune) ve tekrar-çalışma koruması
- Her çalışta geri-yükleme provası: `restic check`, `pg_restore --list`, sonda dosya SHA-256, opsiyonel tam restore
- Admin paneli "Sunucu yedekleri" bölümü: durum, zamanlayıcı, son 10 çalışma, manuel yedek/doğrulama
- Admin API: `status/runs/run/verify/snapshots` + CLI: `necipdrive backup run|verify|status|snapshots|restore`
- Webhook bildirimleri (başarı/başarısız/bayat) + kapsamlı runbook: `docs/BACKUP.md`

### Changed
- Android silme akışı artık "çöp kutusuna taşı" olarak adlandırılıyor (davranış aynı: sunucu zaten soft-delete yapıyordu)
- Android yedekleme yalnız Wi-Fi kuralı, pil muafiyeti istemi ve hız sınırlaması aynen korundu

## 0.8.24 — Android: daima çalışan yedek, tam kütüphane taraması, arka plan güncellemesi

### Added
- Kalıcı yedek servisi: ağ değişimi + yeni medya tetikleyicileri, 15 dakikalık watchdog, tek-uçuş kilidi
- Açılış/güncelleme sonrası otomatik yeniden başlatma (boot + quick-boot + paket değişimi)
- Uzun süre duran yedek için günlük uyarı bildirimi; ayar ekranında arka plan sağlığı satırı
- Sunucu tarafı bütünlük doğrulaması: kaybolan uzak dosyalar yeniden yedek kuyruğuna alınır
- Uygulama kapalıyken bile periyodik güncelleme kontrolü; `minSupportedCode` zorlaması
- İndirilen APK’nın yayın sertifikasıyla eşleşme doğrulaması (imza pinning)

### Changed
- Galeri taraması limitsiz (eski 4000 öğe sınırı eski fotoğrafları atlıyordu); SAF klasörleri derin + tam tarama
- WorkManager artık yalnızca sınırlı yedek görevi; periyodik iş 15 dakikada bir denetçi olarak çalışır
- 401/403 yanıtları “dosya silinmiş” sayılmaz; doğrulama belirsiz cevapta kayıt silmez

### Fixed
- Process ölünce kaybolan Wi‑Fi tetikleyicisi; Android 12+ arka plan servis başlatma hatası
- 24 batch sonrası kapanan yedek döngüsü; periyodik işin sürekli sıfırlanması

## 0.8.23 — Kalıcı cihaz oturumu, kalan güvenlik düzeltmeleri ve Android güncellemesi

### Added
- Android/cihaz token’ları varsayılan olarak süresiz; yalnızca cihaz iptali, çıkış veya şifre sıfırlama ile sonlanır
- WebDAV Basic auth ile cihaz token’ı desteği (2FA kullanıcıları için)
- `/readyz` veritabanı hazır olma endpoint’i
- Sync yüklemelerinde SHA-256 içerik doğrulaması
- WebDAV spool dosyaları için başlangıç/periyodik temizlik ve boyut sınırı
- PostgreSQL entegrasyon testleri (CI): kalıcı cihaz oturumu, recursive trash/purge/kota, changelog drive_id, legacy OTP migrasyonu
- İmzalı Android APK workflow’u: mevcut yayın sertifikasıyla uyum doğrulaması ve `web/public/apps` yayınlama

### Changed
- `DEVICE_TOKEN_TTL=0` varsayılan; pozitif değer isteğe bağlı süre
- Şifre sıfırlama tüm cihaz token’larını da iptal eder
- Klasör purge/restore alt ağacı ve doğru kota sahibini işler
- Paylaşım şifresi denemeleri ve güvenlik onayları hız sınırlandı
- E-posta OTP kodları sunucu secret’ına bağlı HMAC-SHA256 ile saklanır; legacy unsalted challenge’lar migrasyonda kapatılır

### Fixed
- View-only paylaşım bağlantılarında indirme yetkisi bypass’ı kapatıldı
- Android 403 yanıtları oturumu kapatmaz; yalnızca gerçek 401 kimlik iptali sayılır
- Shared drive değişimleri sync üyelerine drive_id ile iletilir

## 0.8.22 — CI/sürüm birliği, depolama temizliği ve güvenlik sertleştirme

### Added
- Tek `VERSION` kaynağı; backend/Docker/Android/release workflow sürüm damgası
- CI (backend/web/Android/Docker) ve tag release workflow
- TOTP (authenticator) 2FA kurulum/etkinleştirme/kapatma; web ayarlar UI
- `TRUSTED_PROXY_CIDRS`, `FILE_CHANGE_RETENTION` yapılandırması
- Android EncryptedSharedPreferences migrasyonu

### Changed
- CSRF token’ları oturuma bağlı HMAC; paylaşım parolaları Argon2id + HMAC unlock cookie
- WebDAV yazmaları RAM yerine disk spool ile stream
- Sync değişim geçmişi budanır; eski cursor güvenli snapshot bootstrap ister

### Fixed
- Versiyon blob GC, çöp kutusu purge sonrası blob temizliği, login limiter sweep
- Public share dosyalarında Range desteği
- Android `allowBackup=false`

## 0.7.2 — Android yedek progress + widget

### Added
- Ana ekranda ince yedekleme progress bar (aktif dosya + kalan)
- Ana ekran widget: TR Driver Yedek durumu

## 0.7.1 — Android in-app APK güncelleme

### Added
- `GET /api/android/version` + `web/public/apps/android-version.json`
- Android: açılışta soft güncelleme diyaloğu; İşlemler → Güncellemeyi kontrol et
- `android/scripts/publish-apk.ps1` — APK + sürüm JSON yayınlama
- Web APK kartı sürümü API’den okur

## Web UI polish — yükleme çubuğu, paylaşım, kart aksiyonları

### Added
- Topbar altında minimal yükleme ilerleme çubuğu; kompakt yükleme dock’u
- Paylaşım: e-posta veya görünen ad ile kullanıcı bulma; SMTP kapalıysa net uyarı

### Fixed
- Sidebar alt (APK/QR/profil) kısa pencerede kırpılma
- Yorumlar paneli dış tık / Escape / Kapat ile kapanır
- Dosya kartlarında ⋮ menü; ayırıcı çizgiler netleştirildi
- Paylaşım e-posta konu satırı UTF-8 encoding

## 0.7.0 — Android UI sadeleştirme

### Added
- Ana ekran: profil hesabı, tek **İşlemler** menüsü, pull-to-refresh, mini müzik çalar
- Yedekleme ayarları ekranı + SAF ile ek klasör yedekleme (`TR Backup / cihaz / klasör`)
- QR bağlantı formu doldurma (`server` / `email` / `password`)
- Upload throttle: aynı anda 1 yükleme, dosyalar arası 1.5–3 sn

### Changed
- Dosya satırında tek ⋮ menü; dosya adları geniş alan
- Galeri yedek paneli ana ekrandan ayrıldı

## Free edition — ücretlendirme kaldırıldı

### Changed
- Koltuk paketleri / yıllık lisans fiyatları iptal; kullanıcı sayısı sınırı yok
- Web «Paketler» menüsü ve admin lisans satın alma UI kaldırıldı
- Plan seed: yalnızca Free aktif (`price_cents=0`); Pro/Team pasif
- Varsayılan kota: `DATA_DIR` disk kapasitesi (`FREE_QUOTA_BYTES=0`); Admin’den ayarlanabilir

## 0.5.0 — WebDAV, paketler UI, mail, bonus kota

### Added
- FS-backed WebDAV (`/dav`) + `packaging/windows/mount-drive.ps1` (Windows `net use` birincil yol)
- Admin SMTP mail ayarları; paylaşım modalında link e-posta gönderme
- Kullanıcı `bonus_quota_bytes` (etkin kota = plan + bonus); Admin Bonus butonu
- Açık/koyu tema (localStorage); kurumsal renk değişkenleri
- Yükleme: görünür progress, aynı isim çakışmasında üzerine yazma onayı + `targetEntryId`
- Paketler görünümü: koltuk lisans kataloğu (`/api/license`); depolama plan kartları kullanıcıdan kaldırıldı

### Changed
- Lisans fiyatları: free 1 kullanıcı; small 2–20 @ 499; medium 21–100 @ 1499; unlimited @ 2999
- Public README / LICENSE_SALES: keygen/private-key satış adımları temizlendi
- `healthz` version → 0.5.0

## 0.4.1 — Lisans talep/yanıt + kurulum kılavuzları

### Added
- Instance-bound license flow: customer `TRDR1` request → vendor `TRD1` response
- Admin: talep kodu üret / etkinleştir; `LICENSE_VENDOR_MODE` ile satıcı yanıt paneli
- `docs/INSTALL_DOCKER.md`, `docs/INSTALL_VPS.md`; lisans satışı dokümanı güncellendi
- CLI: `trdriver-licensegen -request TRDR1...`

## 0.4.0 — TR Driver, lisans, güvenlik

### Added
- Ürün markası: **TR Driver**
- Kullanıcı koltuğu lisansları (`instance_license`, Ed25519 anahtar `TRD1.…`)
  - 1 / 10 / 50 / sınırsız — 99 / 499 / 1499 / 2999 TL / yıl
- Admin lisans etkinleştirme UI + `GET/POST /api/admin/license`, public `GET /api/license`
- Merkezi güncelleme: `GET /api/updates/check` + `UPDATE_MANIFEST_URL`
- `cmd/trdriver-licensegen`
- CONTRIBUTING.md, SECURITY.md, update manifest örneği

### Security
- Public share IDOR (fileId / parentId ağaç sınırı)
- Share browse XSS (innerHTML → textContent / createElement)
- Şifreli share meta sızıntısı azaltıldı; indirme limiti atomik; sabit zamanlı parola karşılaştırması
- Compose dosyalarından hardcode secret kaldırıldı; production placeholder kontrolü
- `content_manager` drive üyelik/silme yükseltemez (`ActionAdminDrive`)
- Device bearer ile admin API engellendi
- İlk admin ataması advisory lock; kayıt koltuk limiti + `ALLOW_REGISTRATION`
- Temel güvenlik başlıkları

## 0.3.0 — Collaboration (Google Drive / OneDrive parity core)

### Added
- Shared Drives (`drives`, `drive_members`) with roles: viewer, commenter, contributor, content_manager, manager
- Central access control (`internal/access`) for personal ownership, drive membership, and inherited ACL
- User-to-user file/folder permissions (`file_permissions`) and Shared with me
- Enhanced public share links: folder browse, password page, expiry, max downloads, list/revoke
- Stars, recent files, server-side name search
- File version snapshots + restore, comments, activity feed, in-app notifications
- Devices settings UI; trash UI; share modal; collab sidebar in web client
- Sync snapshot/changes include shared drive memberships
- MIT LICENSE

### Notes
- Shared drive storage counts against the drive owner’s user quota
- Billing gateway / OAuth / MFA / real-time co-edit intentionally out of scope
