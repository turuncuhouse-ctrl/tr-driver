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
LICENSE_PUBLIC_KEY=   # satıcı public key (sizinki)
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
3. Admin → Lisans → talep kodu üret → satıcıya gönder → `TRD1` yanıtını etkinleştir
4. İsterseniz `ALLOW_REGISTRATION=false`

## 5) Satıcı kendi VPS’i

Sizin (satıcı) sunucuda ek olarak:

```
LICENSE_VENDOR_MODE=true
LICENSE_PRIVATE_KEY=<private-seed-b64>
LICENSE_PUBLIC_KEY=<public-b64>
```

Admin’de **“Satıcı: yanıt lisansı üret”** paneli görünür.

## Güncelleme

```bash
cd /mnt/1tb_disk/necipdrive
git pull
# Portainer → stack Update/Redeploy
```

Volume’lardaki Postgres ve dosya verisi korunur; migration otomatik çalışır.
