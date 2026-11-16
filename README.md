# TR Driver

Kendi sunucunda çalışan, Google Drive’a para ödemeden dosya bulutu. Go + PostgreSQL + React.

**Amaç:** düşük kaynaklı Linux VPS üzerinde self-host; Windows ağ sürücüsü (WebDAV); ortak alanlar, paylaşım, sürümler. **Tamamen ücretsiz** MIT açık kaynak.

## Özellikler

- Kişisel drive, Shared Drives, ACL, paylaşım linkleri, çöp, yıldız, arama
- **Windows ağ sürücüsü:** WebDAV (`/dav`) + `net use` / `packaging/windows/mount-drive.ps1`
- Tray sync istemcisi (ikincil; birincil erişim WebDAV mount)
- Admin paneli (kullanıcı ve kota yönetimi)
- Merkezi güncelleme kontrolü (`UPDATE_MANIFEST_URL`)
- Android istemci + QR giriş; kota varsayılanı disk kapasitesine göre
- Yedekleme işlemi sunucunuzu çok yormayacak şekilde güvenli teker teker yapılmaktadır.
- Sunucu işlemciniz düşükde olsa sunucunuzu dar boğaz yapmayacak şekilde yavaş ve tek tek yapmaktadır.
- **Rehber + SMS yedeği (0.11.0):** Android istemci kişileri ve mesajları yalnız Wi‑Fi üzerinden, artımlı olarak yükler; sunucuda AES‑256‑GCM ile şifreli saklanır, geri yükleme listesi ve tek tıkla silme vardır. Kurulum: [`docs/PERSONAL_BACKUP.md`](docs/PERSONAL_BACKUP.md)
- **LocalTel IP sesli arama (0.13.0):** TR Driver alan adındaki WSS geçidinde sıfır yapılandırmalı çağrı hesabı, rehber eşleştirme ve kilit ekranında sabit **Aç / Reddet** düğmeleri. Türkçe izin sihirbazı ve geçici `ROLE_SMS` ile SMS yedeği geri yükleme. Ayrıntılar: [`docs/LOCALTEL_CALLING.md`](docs/LOCALTEL_CALLING.md)
- **Ortak Paylaşım + torrent (0.14.0):** sunucudaki bir klasör (`/data/paylasim`) yöneticinin klasör bazında izin verdiği kullanıcı ve gruplarla paylaşılır; parolalı, süreli ve indirme sınırlı herkese açık bağlantılar, alan kotası ve doluluk uyarısı, yöneticiye özel magnet/`.torrent` indirme. Web ve Android. Kurulum ve yükseltme notları: [`docs/SHARED_SPACE.md`](docs/SHARED_SPACE.md)
- **Sunucu yedekleri (3-2-1):** her gece şifreli + artımlı `restic` snapshot (veritabanı + dosyalar), S3 offsite replikasyonu, her çalışta geri-yükleme provası, admin paneli + webhook izleme. Kurulum: [`docs/BACKUP.md`](docs/BACKUP.md)

## Kota

Kullanıcı sayısı sınırı yok. Yeni kullanıcı kotası varsayılan olarak **DATA_DIR disk kapasitesine** ayarlanır (`FREE_QUOTA_BYTES=0` / `auto`); Admin panelinden varsayılan kota ve kullanıcı bazlı kota değiştirilebilir.

## Hızlı geliştirme

```bash
# Gerekenler: Go 1.23+, Node 20+, PostgreSQL
export DATABASE_URL=postgres://...
export SESSION_SECRET=en-az-16-karakter-gizli
export SHARE_PASSWORD_SALT=en-az-16-karakter-tuz
cd web && npm ci && npm run build && cd ..
go run ./cmd/server
```

Windows ağ sürücüsü:

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\mount-drive.ps1 -ServerUrl "https://drive.ornek.com" -DriveLetter Z
```

## Docker / Portainer

Compose dosyaları **secret hardcode etmez**. Şunları ortam değişkeni olarak verin:

- `POSTGRES_PASSWORD`
- `SESSION_SECRET` (≥16, placeholder değil)
- `SHARE_PASSWORD_SALT` (≥16 production’da)

Üretimde daha önce commit’lenmiş örnek secret’lar kullanıldıysa **hemen rotate edin**.

İsteğe bağlı:

- `ALLOW_REGISTRATION=true|false`
- `UPDATE_MANIFEST_URL=https://.../trdriver-updates.json`
- `FREE_QUOTA_BYTES=0` (`0` / `auto` / boş = DATA_DIR disk kapasitesi; sabit bayt da verilebilir)
- `FILE_CHANGE_RETENTION=2160h` (sync değişim geçmişi, varsayılan 90 gün)
- `TRUSTED_PROXY_CIDRS=172.18.0.0/16` (yalnızca güvendiğiniz ters vekil ağları; boşsa forwarded header’lar yok sayılır)
- `DEVICE_TOKEN_TTL=0` (cihaz/Android oturumu süresiz; iptal veya şifre sıfırlama ile biter)
- `PERSONAL_BACKUP_KEY` (rehber/SMS yedeğinin sunucudaki şifreleme anahtarı; boşsa `SESSION_SECRET` kullanılır)
- `LOCAL_TEL_KEY` (LocalTel SIP/gateway ayarlarının sunucudaki şifreleme anahtarı; boşsa `PERSONAL_BACKUP_KEY` / `SESSION_SECRET` kullanılır)
- `FIREBASE_SERVICE_ACCOUNT` (isteğe bağlı Firebase service-account JSON; tanımlıysa gelen aramalar için yüksek öncelikli FCM uyanma push'u)
- `SHARED_QUOTA_BYTES`, `SHARED_LINK_MAX_TTL`, `TORRENT_ENABLED`, `TORRENT_LISTEN_PORT`, `TORRENT_*_LIMIT_KBPS` (Ortak Paylaşım ve torrent; tam liste [`docs/SHARED_SPACE.md`](docs/SHARED_SPACE.md))

**0.14.0'a yükseltme:** Compose dosyaları artık `/data/paylasim` volume'unu ve torrent portunu (42069 TCP+UDP) içerir. Stack'i güncellemeden önce sunucuda klasörü oluşturun (`mkdir -p /mnt/1tb_disk/necipdrive/paylasim && chown 1000:1000 /mnt/1tb_disk/necipdrive/paylasim`). Bu klasör yerleşik sunucu yedeğine dahil değildir.

Örnek merkezi güncelleme manifest’i: [`docs/update-manifest.example.json`](docs/update-manifest.example.json)

## Güvenlik

Bakınız [`SECURITY.md`](SECURITY.md). Son denetimde düzeltilen kritik maddeler: public share IDOR, share XSS, compose secret sızıntısı, `content_manager` yetki yükseltmesi, admin’in device token ile erişiminin kapatılması.

## Katkı

[`CONTRIBUTING.md`](CONTRIBUTING.md)

GitHub: https://github.com/turuncuhouse-ctrl/tr-driver

Kurulum:
- [`docs/INSTALL_DOCKER.md`](docs/INSTALL_DOCKER.md) — Docker + Portainer
- [`docs/INSTALL_VPS.md`](docs/INSTALL_VPS.md) — Docker’sız VPS
- [`docs/DEPLOY_VPS.md`](docs/DEPLOY_VPS.md) — secret rotate
- [`docs/GITHUB.md`](docs/GITHUB.md)

## Lisans

Kod: [MIT](LICENSE) — tamamen ücretsiz kullanın, değiştirin, dağıtın.
