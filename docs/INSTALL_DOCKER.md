# TR Driver — Docker ile kurulum

Önerilen yol: **Linux VPS + Docker + Portainer** (veya `docker compose`).

Shared hosting (cPanel/PHP) uygun değildir.

## Dosyaları nasıl aktarırım?

| Yöntem | Ne zaman |
|--------|----------|
| **Git clone / pull** (önerilen) | Güncelleme kolay, tek kaynak: GitHub |
| **Dosya kopyalama (scp/rsync)** | Git yoksa veya air-gapped | 
| Sadece `web/` kopyalamak | **Yetersiz** — Go backend + migration gerekir |

İlk kurulumda tüm repo (veya release tarball) gerekir. Sonra `git pull` + redeploy yeter.

```bash
# VPS örneği
cd /mnt/1tb_disk
git clone https://github.com/turuncuhouse-ctrl/tr-driver.git necipdrive
cd necipdrive
```

Mevcut klasörünüz varsa: dosyaları üstüne kopyalayın **veya** `git remote add` + `pull`. `data/` ve `postgres/` volume’larını silmeyin.

## 1) Secret üret

```bash
openssl rand -base64 32   # SESSION_SECRET
openssl rand -base64 32   # SHARE_PASSWORD_SALT
# Postgres zaten varsa: mevcut DB parolasını kullanın
```

Portainer Environment / `.env`:

```
POSTGRES_PASSWORD=...
SESSION_SECRET=...
SHARE_PASSWORD_SALT=...
PUBLIC_BASE_URL=https://drive.ornek.com
ALLOW_REGISTRATION=true
FREE_QUOTA_BYTES=0
```

## 2) Compose seçenekleri

- Geliştirme/kolay: `docker-compose.easy.yml` (kaynak mount + `go build`)
- Image: `docker-compose.stack.yml` / `docker-compose.portainer.yml`
- Tam paket: `deploy/docker-compose.yml` (+ Caddy)

## 3) NPM / ters vekil

Domain → `http://SUNUCU_IP:3080` (veya compose portu). HTTPS Let’s Encrypt açık olsun.

## 4) İlk giriş

1. `https://domain/healthz` → `{"status":"ok","product":"TR Driver",...}`
2. Web UI’de ilk kullanıcı = admin
3. Admin → Varsayılan kota (disk kapasitesi) ve kullanıcı kotalarını istediğiniz gibi ayarlayın
4. İsterseniz `ALLOW_REGISTRATION=false`

## Windows ağ sürücüsü (WebDAV)

Birincil Windows erişimi: WebDAV path `/dav`.

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\mount-drive.ps1 -ServerUrl "https://drive.ornek.com" -DriveLetter Z
```

veya: `net use Z: https://drive.ornek.com/dav /user:EMAIL PASSWORD`

HTTPS önerilir. Tray sync istemcisi ikincildir.

## Güncelleme

```bash
cd /mnt/1tb_disk/necipdrive
git pull
# Portainer → stack Update/Redeploy — healthz version kontrol edin
```

Volume’lardaki Postgres ve dosya verisi korunur; migration otomatik çalışır.
