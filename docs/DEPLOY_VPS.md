# TR Driver — VPS deploy + secret rotate

## Hosting mi, VPS mi?

**VPS / VDS kullanın** (sizin mevcut Linux sunucu gibi).

| | Shared hosting | VPS/VDS |
|--|----------------|---------|
| Docker / Go / Postgres | Genelde yok | Tam kontrol |
| Kalıcı disk + yedek | Sınırlı | Uygun |
| Portainer / Nginx Proxy Manager | Zor | Sizin kurulumunuza uygun |

TR Driver self-host bulut deposu; **VPS doğru model**. Managed “app hosting” (Fly/Railway) da olur ama maliyet/kontrol için mevcut VPS daha mantıklı.

## Canlı sunucu (Portainer) checklist

Mevcut stack: `/mnt/1tb_disk/necipdrive`, domain `drive.neciparmagan.net.tr`.

### 1) Yeni secret üret (Windows veya VPS)

```bash
openssl rand -base64 32   # SESSION_SECRET
openssl rand -base64 32   # SHARE_PASSWORD_SALT
openssl rand -base64 24   # POSTGRES_PASSWORD (yalnızca YENİ db ise)
```

**Önemli:** Postgres data volume zaten varsa `POSTGRES_PASSWORD` env’i **ilk init’ten sonra** DB şifresini değiştirmez.  
Eski DB için `DATABASE_URL` içindeki parola, **mevcut** postgres şifresi olmalı.

- Eski compose’da `NpDrv_Pg_...` vardıysa: ya o değeri `POSTGRES_PASSWORD` / `DATABASE_URL` olarak geçici verip uygulamayı ayağa kaldırın, **ya da** Postgres içinde `ALTER USER` ile yeni şifreye geçin.
- Eski `SESSION_SECRET` değişince tüm oturumlar düşer (normal).
- Eski share salt değişince eski şifreli paylaşımların parolaları bozulur → share’leri yeniden oluşturun.

### 2) Portainer stack env

```
POSTGRES_PASSWORD=<db-parolası>
SESSION_SECRET=<yeni-uzun-secret>
SHARE_PASSWORD_SALT=<yeni-uzun-tuz>
PUBLIC_BASE_URL=https://drive.neciparmagan.net.tr
ALLOW_REGISTRATION=true
LICENSE_PUBLIC_KEY=<ürettiğiniz-public-b64>
UPDATE_MANIFEST_URL=   # sonra GitHub raw/release URL
```

Compose dosyaları artık secret hardcode etmiyor; env zorunlu.

### 3) Kod güncelle + restart

```bash
cd /mnt/1tb_disk/necipdrive
# git pull  (repo bağlandıktan sonra)
# veya dosyaları kopyala
```

Portainer’da stack’i **Update / Redeploy**. İlk boot migration `instance_license` tablosunu açar.

### 4) Doğrulama

```bash
curl -s https://drive.neciparmagan.net.tr/healthz
curl -s https://drive.neciparmagan.net.tr/api/license
```

Admin panel → Lisans: test anahtarı etkinleştir.

### 5) Sertleştirme (kullanıma açınca)

- İlk admin kaydından sonra `ALLOW_REGISTRATION=false` (veya koltuk dolunca)
- HTTPS (NPM Let’s Encrypt) açık kalsın
- Eski commit’te sızmış secret’lar hâlâ kullanılıyorsa **mutlaka rotate**

## Merkezi güncelleme

`docs/update-manifest.example.json` dosyasını GitHub Release / raw’a koyun; sunucuda:

```
UPDATE_MANIFEST_URL=https://raw.githubusercontent.com/<user>/tr-driver/main/docs/update-manifest.example.json
```

İstemciler / admin `GET /api/updates/check` ile bakar.
