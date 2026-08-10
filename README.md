# TR Driver

Kendi sunucunda çalışan, Google Drive’a para ödemeden dosya bulutu. Go + PostgreSQL + React.

**Amaç:** düşük kaynaklı Linux VPS üzerinde self-host; Windows senkron istemcisi; ortak alanlar, paylaşım, sürümler; kullanıcı sayısı bazlı ticari lisans.

## Özellikler

- Kişisel drive, Shared Drives, ACL, paylaşım linkleri, çöp, yıldız, arama
- Windows tray sync istemcisi (WebView2)
- Admin paneli + **kullanıcı koltuğu lisansları** (1 / 10 / 50 / sınırsız)
- Merkezi güncelleme kontrolü (`UPDATE_MANIFEST_URL`)
- MIT açık kaynak; lisans anahtarı ile koltuk limiti açılır

## Lisans fiyatları (yıllık, TL)

| Paket | Kullanıcı | Fiyat |
|-------|-----------|-------|
| personal | 1 | 99 TL |
| small | 1–10 | 499 TL |
| medium | 11–50 | 1499 TL |
| unlimited | 50+ | 2999 TL |

Lisanssız kurulumda **1 kullanıcı** (ilk admin) oluşturulabilir. Anahtar: Admin paneli → Lisans.

Anahtar üretimi (satıcı tarafı):

```bash
# Geliştirme / test (RFC örnek anahtarı — satış için KULLANMAYIN)
set LICENSE_ALLOW_DEV_SIGNING=1
go run ./cmd/trdriver-licensegen -tier small -years 1 -customer "Acme"

# Üretim: kendi Ed25519 seed'iniz
set LICENSE_PRIVATE_KEY=<base64-32-byte-seed>
set LICENSE_PUBLIC_KEY=<base64-32-byte-public>   # sunucu doğrulama (müşteri sunucusunda da)
go run ./cmd/trdriver-licensegen -tier unlimited -years 1 -customer "Musteri A.S."
```

## Hızlı geliştirme

```bash
# Gerekenler: Go 1.23+, Node 20+, PostgreSQL
export DATABASE_URL=postgres://...
export SESSION_SECRET=en-az-16-karakter-gizli
export SHARE_PASSWORD_SALT=en-az-16-karakter-tuz
cd web && npm ci && npm run build && cd ..
go run ./cmd/server
```

Windows sync:

```bash
go build -ldflags="-H windowsgui" -o dist/windows/necipdrive-sync.exe ./cmd/necipdrive-sync
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
- `LICENSE_PUBLIC_KEY=...` (kendi imza anahtarınız)

Örnek merkezi güncelleme manifest’i: [`docs/update-manifest.example.json`](docs/update-manifest.example.json)

## Güvenlik

Bakınız [`SECURITY.md`](SECURITY.md). Son denetimde düzeltilen kritik maddeler: public share IDOR, share XSS, compose secret sızıntısı, `content_manager` yetki yükseltmesi, admin’in device token ile erişiminin kapatılması.

## Katkı

[`CONTRIBUTING.md`](CONTRIBUTING.md)

## Lisans (yazılım)

Kod: [MIT](LICENSE). Ticari **koltuk lisansı** ayrıdır (anahtar ile etkinleştirilir).
