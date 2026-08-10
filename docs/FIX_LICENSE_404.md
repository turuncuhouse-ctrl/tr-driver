# Talep kodu 404 — zorunlu redeploy

## Teşhis (canlı sitede doğrulandı)

`https://drive.neciparmagan.net.tr/healthz` şu an dönüyor:

```json
{"status":"ok"}
```

Yeni TR Driver şunu dönmeli:

```json
{"status":"ok","product":"TR Driver","version":"0.4.2"}
```

`/api/license` de 404 — yani **çalışan container ESKİ binary**. Klasöre dosya koymak yetmez; **yeniden build + recreate** gerekir.

## Easy compose (kaynak mount) kullanıyorsanız

```bash
cd /mnt/1tb_disk/necipdrive
git pull origin main
# veya Windows’tan güncel klasörü buraya tekrar kopyalayın

# Portainer → Stack → Stop → Start
# veya:
docker compose -f docker-compose.easy.yml down
docker compose -f docker-compose.easy.yml up -d
```

Container log’da şunu görmelisiniz: `Built TR Driver 0.4.2`

Sonra:

```bash
curl -s https://drive.neciparmagan.net.tr/healthz
# version alanı zorunlu
```

Tarayıcıda **Ctrl+F5** → Admin → Talep kodu üret.

## Image/stack kullanıyorsanız (kaynak mount yok)

```bash
cd /mnt/1tb_disk/necipdrive
git pull
docker build --no-cache -t trdriver:latest .
# Portainer stack’te image: trdriver:latest → Update / Redeploy + re-pull
```

## Hâlâ 404 ise

1. NPM yanlış porta mı gidiyor? (başka eski container?)
2. `docker ps` ile hangi image/command çalışıyor kontrol edin
3. Container içinden: `wget -qO- http://127.0.0.1:8080/healthz`
