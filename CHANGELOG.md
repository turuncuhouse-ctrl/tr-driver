# Changelog

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
