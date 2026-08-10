# Portainer + Nginx Proxy Manager Kurulumu

Domain: `drive.neciparmagan.net.tr`
Uygulama portu: **3080**
Proje yolu: `/mnt/1tb_disk/necipdrive`

## Hangi dosyayi kullanmaliyim?

| Dosya | Ne zaman | Build gerekir mi |
|-------|----------|------------------|
| `docker-compose.easy.yml` | Portainer Web editor, en kolay yol | Hayir |
| `docker-compose.stack.yml` | Hazir image ile calistirmak | Evet (SSH'da bir kez) |
| `docker-compose.portainer.yml` | Portainer Repository (Git) yontemi | Portainer yapar |

---

## YOL 1 (onerilen): En kolay - build yok

Sunucuda SSH ile bir kez klasorleri olustur:

```bash
mkdir -p /mnt/1tb_disk/necipdrive/data /mnt/1tb_disk/necipdrive/postgres
```

Sonra Portainer'da:

1. Stacks > Add stack
2. Name: `necipdrive`
3. Build method: **Web editor**
4. `docker-compose.easy.yml` iceriginin tamamini yapistir
5. Deploy the stack

Bu yontemde `golang:1.25-alpine` image'i kullanilir ve kaynak kod
`/mnt/1tb_disk/necipdrive` icinden mount edilir. `docker build` gerekmez,
image indirme hatasi (`pull access denied`) olusmaz.

Ilk baslangicta Go bagimliliklari indirilip derlenir: **1-3 dakika**.
Loglari izle: Portainer > Containers > `necipdrive-app-1` > Logs.
`listening on :8080` satirini gorunce hazirdir.

Sonraki yeniden baslatmalar cache sayesinde daha hizlidir.

---

## YOL 2: Derlenmis image ile (daha hizli calisir)

```bash
mkdir -p /mnt/1tb_disk/necipdrive/data /mnt/1tb_disk/necipdrive/postgres
chown -R 1000:1000 /mnt/1tb_disk/necipdrive/data

cd /mnt/1tb_disk/necipdrive
docker build -t necipdrive:latest .
docker images | grep necipdrive     # necipdrive latest gorunmeli
```

`docker images` listesinde image gorunmuyorsa build basarisiz olmustur;
Portainer'da stack'i deploy etmek `pull access denied` hatasi verir.

Image hazirsa Portainer > Stacks > Add stack > Web editor ile
`docker-compose.stack.yml` iceriginin tamamini yapistir ve Deploy et.

Tek komutla ayni isi yapan yardimci script:

```bash
cd /mnt/1tb_disk/necipdrive
sh deploy/install.sh
```

---

## Nginx Proxy Manager

Yeni Proxy Host:

| Alan | Deger |
|------|--------|
| Domain Names | `drive.neciparmagan.net.tr` |
| Scheme | `http` |
| Forward Hostname / IP | sunucu IP'si |
| Forward Port | `3080` |
| Block Common Exploits | acik |
| SSL | Request a new SSL Certificate (Let's Encrypt) |
| Force SSL | acik |
| HTTP/2 | acik |

DNS: `drive.neciparmagan.net.tr` -> sunucu A kaydi.

Buyuk dosya yuklemeleri icin NPM > Advanced:

```
client_max_body_size 16m;
proxy_request_buffering off;
proxy_buffering off;
proxy_read_timeout 300s;
proxy_send_timeout 300s;
```

Uygulama dosyalari 8 MiB parçalar halinde yukler. NPM artik 10 GB'lik
tek istegi tamponlamaz; yalnizca kisa parcalari gorur.

---

## Kontrol

```bash
curl http://127.0.0.1:3080/healthz
```

Tarayici: `https://drive.neciparmagan.net.tr`

## Veri konumlari

- Dosyalar: `/mnt/1tb_disk/necipdrive/data`
- PostgreSQL: `/mnt/1tb_disk/necipdrive/postgres`

## Guncelleme

YOL 1 kullaniyorsan: kodu guncelle, Portainer'da konteyneri **Restart** et.

YOL 2 kullaniyorsan:

```bash
cd /mnt/1tb_disk/necipdrive
docker build -t necipdrive:latest .
docker compose -f docker-compose.stack.yml up -d
```

## Sorun giderme

| Hata | Sebep ve cozum |
|------|----------------|
| `pull access denied for necipdrive` | Image yok. `docker build -t necipdrive:latest .` yap veya YOL 1'e gec |
| `unable to prepare context: path not found` | Portainer host klasorunu goremez. Web editor'de build kullanma; YOL 1 veya YOL 2 |
| 502 Bad Gateway | App henuz derleniyor/basliyor. Loglari izle, 1-3 dk bekle |
| Port cakismasi | `3080:8080` yerine baska port sec, NPM Forward Port'u da guncelle |
| Izin hatasi (dosya yazma) | `chown -R 1000:1000 /mnt/1tb_disk/necipdrive/data` |
| Buyuk dosya yuklenmiyor | NPM Advanced ayarlarini 16m + timeout 300s yap; app loglarina bak |
| Sunucu baglantisi kesildi | Parcali yukleme otomatik yeniden dener; ayni klasoru yeniden secip devam et |
| Published ports bos / konteyner surekli yeniden basliyor | App calismadan cikmis. Loglara bak: `docker logs necipdrive-app-1 --tail 80` |
| `go.mod requires go >= 1.23.0` | Eski `golang:1.22-alpine` kullaniliyor. Stack'i guncel `docker-compose.easy.yml` ile yeniden deploy et |

### Hizli teshis

```bash
docker ps -a | grep necipdrive
docker logs necipdrive-app-1 --tail 80
```

Stack'i guncel compose ile yeniden olustur:

```bash
cd /mnt/1tb_disk/necipdrive
docker compose -f docker-compose.easy.yml up -d --force-recreate
```
