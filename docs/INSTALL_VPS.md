# TR Driver — Docker’sız / VPS (binary + systemd)

“Hosting” derken **shared PHP hosting değil**, kendi VPS’inizde Docker olmadan kurulum.

## Gereksinimler

- Linux VPS (Ubuntu 22.04+ önerilir)
- PostgreSQL 16
- Go 1.23+ (build için) veya hazır binary
- Node 20+ (web build için)
- Nginx veya Caddy (HTTPS)

## Aktarım

Tüm proje klasörünü kopyalayın (`scp -r`, `rsync`) **veya** `git clone`.  
Sadece frontend kopyalamak yetmez.

```bash
rsync -avz --exclude node_modules --exclude data \
  ./ user@vps:/opt/tr-driver/
```

## PostgreSQL

```bash
sudo -u postgres createuser trdriver password 'GUCLU_PAROLA'
sudo -u postgres createdb -O trdriver trdriver
```

## Build

```bash
cd /opt/tr-driver/web && npm ci && npm run build && cd ..
go build -o /opt/tr-driver/bin/trdriver ./cmd/server
```

## Env dosyası `/etc/trdriver.env`

```
APP_ENV=production
HTTP_ADDR=127.0.0.1:8080
DATABASE_URL=postgres://trdriver:GUCLU_PAROLA@127.0.0.1:5432/trdriver?sslmode=disable
SESSION_SECRET=...
SHARE_PASSWORD_SALT=...
DATA_DIR=/var/lib/trdriver/files
PUBLIC_BASE_URL=https://drive.ornek.com
ALLOW_REGISTRATION=true
FREE_QUOTA_BYTES=0
```

```bash
sudo mkdir -p /var/lib/trdriver/files
sudo useradd -r -s /usr/sbin/nologin trdriver || true
sudo chown -R trdriver:trdriver /var/lib/trdriver /opt/tr-driver
```

## systemd

`/etc/systemd/system/trdriver.service`:

```ini
[Unit]
Description=TR Driver
After=network.target postgresql.service

[Service]
Type=simple
User=trdriver
WorkingDirectory=/opt/tr-driver
EnvironmentFile=/etc/trdriver.env
ExecStart=/opt/tr-driver/bin/trdriver
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now trdriver
```

## Nginx örneği

```nginx
server {
  server_name drive.ornek.com;
  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    client_max_body_size 0;
  }
}
```

Certbot ile HTTPS ekleyin.

## Windows ağ sürücüsü (WebDAV)

HTTPS sonrası:

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\mount-drive.ps1 -ServerUrl "https://drive.ornek.com" -DriveLetter Z
```

Sunucu yolu: `/dav`. Tray sync ikincildir.

## Kota

Varsayılan kota `DATA_DIR` disk kapasitesine göre ayarlanır (`FREE_QUOTA_BYTES=0`). Admin panelinden varsayılan ve kullanıcı kotası değiştirilebilir.
