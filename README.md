# TR Driver

Kendi sunucunda çalışan, Google Drive’a para ödemeden dosya bulutu. Go + PostgreSQL + React.

**Amaç:** düşük kaynaklı Linux VPS üzerinde self-host; Windows ağ sürücüsü (WebDAV); ortak alanlar, paylaşım, sürümler; kullanıcı sayısı bazlı ticari lisans.

## Özellikler

- Kişisel drive, Shared Drives, ACL, paylaşım linkleri, çöp, yıldız, arama
- **Windows ağ sürücüsü:** WebDAV (`/dav`) + `net use` / `packaging/windows/mount-drive.ps1`
- Tray sync istemcisi (ikincil; birincil erişim WebDAV mount)
- Admin paneli + **kullanıcı koltuğu lisansları** (1 / 2–20 / 21–100 / sınırsız)
- Merkezi güncelleme kontrolü (`UPDATE_MANIFEST_URL`)
- MIT açık kaynak; lisans anahtarı ile koltuk limiti açılır

## Lisans fiyatları (yıllık, TL)

| Paket | Kullanıcı | Fiyat |
|-------|-----------|-------|
| personal | 1 | Ücretsiz |
| small | 2–20 | 499 TL |
| medium | 21–100 | 1499 TL |
| unlimited | 1000+ | 2999 TL |

Lisanssız kurulumda **1 kullanıcı** (ilk admin) oluşturulabilir.

### Müşteri aktivasyonu

1. Admin → Lisans → paket seç → **talep kodu üret** (`TRDR1…`)
2. Talebi satıcıya iletin
3. Gelen yanıt anahtarını (`TRD1…`) Admin’e yapıştırıp **etkinleştirin**
4. Müşteri sunucusunda doğrulama için satıcının verdiği `LICENSE_PUBLIC_KEY` kullanılır

Satıcı tarafı yanıt üretimi yerel araçla yapılır (private key asla GitHub’a veya müşteri sunucusuna konmaz). Ayrıntı: [`docs/LICENSE_SALES.md`](docs/LICENSE_SALES.md).

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
- `LICENSE_PUBLIC_KEY=...` (satıcının public anahtarı)

Örnek merkezi güncelleme manifest’i: [`docs/update-manifest.example.json`](docs/update-manifest.example.json)

## Güvenlik

Bakınız [`SECURITY.md`](SECURITY.md). Son denetimde düzeltilen kritik maddeler: public share IDOR, share XSS, compose secret sızıntısı, `content_manager` yetki yükseltmesi, admin’in device token ile erişiminin kapatılması.

## Katkı

[`CONTRIBUTING.md`](CONTRIBUTING.md)

GitHub: https://github.com/turuncuhouse-ctrl/tr-driver

Deploy / kurulum / lisans:
- [`docs/INSTALL_DOCKER.md`](docs/INSTALL_DOCKER.md) — Docker + Portainer
- [`docs/INSTALL_VPS.md`](docs/INSTALL_VPS.md) — Docker’sız VPS
- [`docs/DEPLOY_VPS.md`](docs/DEPLOY_VPS.md) — secret rotate
- [`docs/LICENSE_SALES.md`](docs/LICENSE_SALES.md) — talep/yanıt lisans
- [`docs/GITHUB.md`](docs/GITHUB.md)

## Lisans (yazılım)

Kod: [MIT](LICENSE). Ticari **koltuk lisansı** ayrıdır (anahtar ile etkinleştirilir).
