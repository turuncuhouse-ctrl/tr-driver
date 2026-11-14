# TR Driver — Sunucu Yedekleme ve Felaket Kurtarma

Bu belge TR Driver sunucusunun **3-2-1 yedekleme sistemini** anlatır: her gece otomatik
alınan, şifreli, artımlı (incremental) yedekler + her çalışta geri-yükleme provası.

## 1. Mimari

```
┌─────────────┐   pg_dump -Fc   ┌──────────────────────┐
│  PostgreSQL │ ───────────────▶│  restic yerel repo   │  /data/backups/restic
│  (metadata) │                 │  AES-256 şifreli     │  (2. disk/klasör)
└─────────────┘                 │  artımlı snapshot    │
┌─────────────┐   restic        │  keep 7/4/6          │
│  DATA_DIR   │ ───────────────▶│                      │
│  (blob'lar) │                 └──────────┬───────────┘
└─────────────┘                              │ aynı snapshot
                                             ▼
                                ┌──────────────────────┐
                                │  restic S3 repo      │  offsite (Wasabi/B2/
                                │  (opsiyonel)         │  Hetzner/MinIO…)
                                └──────────────────────┘
```

- **Motor:** [restic](https://restic.net) — kendini kanıtlamış, şifreli, tekilleştirmeli
  (deduplication) yedekleme aracı. TR Driver onu orkestre eder; tekerleği yeniden
  icat etmez.
- **Veritabanı:** her çalışta `pg_dump --format=custom` alınır, snapshot'a `db.dump`
  olarak girer. Custom format sıkıştırmalıdır ve `pg_restore --list` ile çevrimdışı
  doğrulanabilir.
- **Dosyalar:** `DATA_DIR` tamamı (`tmp/` ve `lost+found` hariç). İlk yedek tam,
  sonrakiler **yalnızca değişen blokları** gönderir — 500 GB veride bile gece
  çalışması dakikalar sürer (değişim kadar).
- **Saklama:** varsayılan `keep-daily 7 / keep-weekly 4 / keep-monthly 6`, iki repoda
  da `forget --prune` ile uygulanır.
- **RPO/RTO:** RPO ≈ 24 saat (gece yarısı çalışması); RTO saat mertebesi (indirme +
  `pg_restore` + dosya taşıma). Dakika-mertebesi RPO için WAL arşivleme yol
  haritasındadır (bkz. §9).

## 2. Kurulum (Portainer)

### 2.1 Parola üret

Repo parolası **yedeklerin şifreleme anahtarıdır**. Kaybederseniz yedekler
kurtarılamaz — parola yöneticisine + çevrimdışı bir kopyaya yazın.

```bash
openssl rand -base64 32
```

### 2.2 Stack env'ine ekle

Portainer → Stacks → tr-driver → Environment variables:

| Değişken | Örnek | Açıklama |
|---|---|---|
| `BACKUP_PASSWORD` | `openssl` çıktısı | **Zorunlu.** <16 karakterse sunucu açılmaz |
| `BACKUP_SCHEDULE` | `02:00` | Her gün çalışma saati (sunucu saati) |
| `TZ` | `Europe/Istanbul` | Zamanlayıcının kullandığı saat dilimi |
| `BACKUP_KEEP_DAILY/WEEKLY/MONTHLY` | `7/4/6` | Snapshot saklama |
| `BACKUP_MAX_AGE` | `30h` | Bu süredir başarılı yedek yoksa "bayat" alarmı |
| `BACKUP_WEBHOOK_URL` | `https://ntfy.sh/trdriver-yedek-gizli` | Başarı/başarısızlık/bayat bildirimi |
| `BACKUP_RUN_ON_BOOT` | `false` | `true` ise kesinti sonrası açılışta telafi çalışması |
| `BACKUP_S3_*` | bkz. §4 | Offsite replikasyon (boşsa yalnızca yerel) |

Compose dosyaları (`docker-compose.portainer.yml` vb.) bu değişkenleri ve
`/mnt/1tb_disk/necipdrive/backups:/data/backups` mount'unu zaten içerir.
**Önemli:** yedek klasörü veri klasöründen **farklı bir disk/klasör** olmalı;
aynı diskteki yedek, disk arızasına karşı koruma sağlamaz.

### 2.3 İlk çalıştırma

1. Stack'i **Update the stack** ile yeniden deploy edin.
2. Admin paneli → **Sunucu yedekleri** → Araçlar "Hazır" olmalı
   (`restic ✓ · pg_dump ✓ · pg_restore ✓`).
3. **Şimdi yedekle**'ye basın. İlk yedek veri boyutuna göre uzun sürebilir;
   ilerleme `docker logs` içinde `backup run … started/finished` satırlarından
   izlenir.
4. Sonuç **✓ Başarılı** + `geri-yükleme ✓` olmalı. Değilse hata metnini okuyun
   (bkz. §7).

`BACKUP_PASSWORD` boş bırakılırsa sunucu normal açılır ama zamanlayıcı
**kapalı** kalır ve durum ekranında sebebi görünür. Mevcut kurulumlar bu
yüzden güncellemeden etkilenmez.

## 3. İzleme

- **Admin paneli → Sunucu yedekleri:** durum, son başarılı, sonraki çalışma,
  offsite, araç kontrolü, son 10 çalışmanın tablosu, manuel **Şimdi yedekle** ve
  **Doğrula** düğmeleri.
- **API (admin oturumu gerekir):**
  - `GET /api/admin/backup/status` — `stale`, `running`, `nextRun`, `binaries`
  - `GET /api/admin/backup/runs?limit=20` — çalış geçmişi
  - `POST /api/admin/backup/run` — 202 + `runId` (çakışırsa 409)
  - `POST /api/admin/backup/verify` — yeni snapshot almadan provayı tekrarla
  - `GET /api/admin/backup/snapshots?repo=local|offsite` — snapshot listesi
- **Webhook:** her çalışta (başarı/kısmi/başarısız) ve günde en fazla bir kez
  "bayat" uyarısı POST edilir. En kolay ücretsiz alıcı:
  [ntfy.sh](https://ntfy.sh) — `BACKUP_WEBHOOK_URL=https://ntfy.sh/<gizli-konu>`,
  telefona ntfy uygulamasıyla anında bildirim gelir.
- **Bayat kuralı:** `BACKUP_MAX_AGE` (varsayılan 30 saat) içinde başarılı yedek yoksa
  durum `stale=true` olur, panelde kırmızı uyarı çıkar ve webhook gönderilir.

## 4. Offsite S3 replikasyonu (3-2-1'in 3. kopyası)

Bucket + erişim anahtarları doluysa her gece aynı snapshot S3'e de yazılır.
Şifreleme istemcide yapılır; S3 tarafı yalnızca şifreli blob görür.

| Değişken | Açıklama |
|---|---|
| `BACKUP_S3_ENDPOINT` | S3-uyumlu uç (boş = gerçek AWS). Örn. `https://s3.eu-central-1.wasabisys.com` |
| `BACKUP_S3_REGION` | Bölge (boş = `us-east-1`) |
| `BACKUP_S3_BUCKET` | Bucket adı (**önceden oluşturun**) |
| `BACKUP_S3_PREFIX` | Repo öneki (varsayılan `trdriver`) |
| `BACKUP_S3_ACCESS_KEY` / `BACKUP_S3_SECRET_KEY` | Erişim anahtarları |

Örnekler:

- **Backblaze B2:** endpoint `https://s3.eu-central-003.backblazeb2.com`, bucket önceden açılır.
- **Wasabi:** endpoint `https://s3.eu-central-1.wasabisys.com`.
- **Hetzner Object Storage:** endpoint `https://<id>.your-objectstorage.com`.
- **Kendi MinIO'nuz:** endpoint `http://minio.lan:9000`, bucket önceden açılır.
- **AWS S3:** endpoint boş, region örn. `eu-central-1`.

Notlar:

- restic S3'e standart `AWS_*` değişkenleriyle bağlanır; endpoint `-o s3.endpoint`
  ile verilir. Çoğu sağlayıcıda ek ayar gerekmez.
- Offsite doğrulaması bant genişliği yakmamak için **metadata check** ile yapılır;
  bayt-bayt geri-yükleme provası yerel snapshot üzerinde çalışır (kaynak aynı).
- Offsite başarısız olursa çalış **Kısmi (partial)** işaretlenir; yerel yedek
  korunur. Hata metni panelde ve webhook'ta görünür.

## 5. Geri yükleme (felaket senaryosu)

### 5.1 Tek dosya / tek klasör

En kolayı restic'i doğrudan kullanmaktır (konteyner içinde veya yedek klasörünü
gören herhangi bir makinede):

```bash
export RESTIC_REPOSITORY=/mnt/1tb_disk/necipdrive/backups/restic
export RESTIC_PASSWORD='...'
# snapshot'ları listele
restic snapshots
# tek dosyayı bul ve geri yükle
restic find --snapshot latest rapor.pdf
restic restore latest --target /tmp/geri --include /data/files/kullanici/rapor.pdf
```

### 5.2 Tam felaket (sıfırdan kurulum)

1. Yeni VPS'e Portainer + stack'i kurun (**henüz başlatmayın**), `BACKUP_*`
   değişkenlerini ve yedek klasörünü/S3 erişimini aynı şekilde verin.
2. Snapshot'ı bir çalışma klasörüne açın:

```bash
docker exec -it <app-konteyneri> /app/necipdrive backup restore \
  --repo local --snapshot latest --target /tmp/geri
# veya offsite'tan: --repo offsite (BACKUP_S3_* gerekli)
```

3. Veritabanını geri yükleyin (`db.dump` hedef klasörün altındadır):

```bash
docker exec -it <app-konteyneri> sh -c \
  'pg_restore --dbname "$DATABASE_URL" --no-owner --clean /tmp/geri/.../db.dump'
```

4. Dosya ağacını yerine koyun:

```bash
# /tmp/geri altındaki data/files içeriğini DATA_DIR'e taşıyın/kopyalayın,
# sahipliği koruyun (konteyner içi kullanıcı appuser, uid 1000).
```

5. Stack'i başlatın, `/healthz` ve girişi doğrulayın, ardından
   `necipdrive backup verify` ile provayı tekrarlayın.

### 5.3 Tatbikat önerisi

Yedeklenmeyen değil, **geri yüklenemeyen** veri kaybolur. Ayda bir:

1. Admin panelinden **Doğrula** çalıştırın (otomatik zaten her gece yapıyor).
2. Çeyrekte bir §5.2'nin 2–3. adımlarını **boş bir test klasörüne + ayrı bir
   test veritabanına** uygulayın.

## 6. Otomatik doğrulama ("geri-yükleme provası") nedir?

Her çalışmanın sonunda, alınan snapshot gerçekten kullanılır:

1. `restic check` — repo bütünlüğü (iki repoda da).
2. `db.dump` snapshot'tan geri yüklenir → `pg_restore --list` ile okunabilirliği
   kanıtlanır.
3. Önceki çalışta hash'i kaydedilen küçük bir **sonda dosyası** (probe) geri
   yüklenir → SHA-256 karşılaştırılır. Uyuşmazsa çalış başarısız sayılır.
4. (Opsiyonel `BACKUP_VERIFY_RESTORE=true`) dump, atılabilir bir veritabanına
   tamamen geri yüklenir + `select count(*) from users` çalışır. Bu mod
   `CREATEDB` yetkisi ister: `ALTER USER necipdrive CREATEDB;`

"Doğrula" düğmesi ve `necipdrive backup verify` aynı provayı yeni snapshot
almadan tekrarlar.

## 7. Sorun giderme

| Belirti | Neden / Çözüm |
|---|---|
| Araçlar "Eksik" | İmaj güncel değil → stack'i rebuild/redeploy edin (`restic`, `postgresql-client` imajda gelir) |
| `BACKUP_PASSWORD is not set` | Stack env'e parola ekleyip redeploy edin |
| `pg_dump … connection refused` | `DATABASE_URL` yanlış veya `db` servisi sağlıksız |
| `offsite backup: … 403/NoSuchBucket` | Bucket adı/anahtar/endpoint hatası; bucket'ı sağlayıcıda önceden açın |
| Çalış `Kısmi` | Detay hata metninde: genelde offsite veya retention uyarısı; yerel snapshot sağlamdır |
| Disk doluyor | Saklamayı düşürün (`BACKUP_KEEP_*`) veya yedek diskini büyütün; `restic` prune her çalışta otomatik çalışır |
| `stale=true` | Zamanlayıcı çalışmıyor (parola eksik?) veya son çalışma başarısız; paneldeki hataya bakın |
| Şifreyi unuttum | **Kurtarma yok.** Yeni parola = yeni boş repo; eski snapshot'lar okunamaz. Parolayı parola yöneticisi + çevrimdışı kopya ile saklayın |

## 8. Güvenlik notları

- Yedekler **uçtan uca şifrelidir** (restic AES-256-Poly1305); S3'e ve yedek
  diskine erişen biri parola olmadan içeriği okuyamaz.
- `BACKUP_PASSWORD` ve `BACKUP_S3_SECRET_KEY` **secret'tır**: Portainer env ile
  verin, repoya/görüntüye gömmeyin, chat/e-postayla paylaşmayın.
- `BACKUP_PASSWORD`'u `SESSION_SECRET`'ten **farklı** tutun (biri sızarsa diğeri
  korunur).
- Webhook URL'si gizli bir konu/token içermeli (herkese açık ntfy konusu
  kullanmayın).
- Parolayı değiştirmeniz gerekirse: eski parolayla `restic key passwd` (iki
  repoda da) veya yeni parola + yeni repo başlatın.

## 9. Yol haritası (bu PR'ın dışında)

- **WAL arşivleme + PITR:** dakika-mertebesi RPO için `archive_mode=on` ve
  `pg_basebackup` tabanlı taban yedekleri. Postgres konteyner yapılandırması
  gerektirir; `backup_runs.kind` (`logical`/`base`/`wal`) genişlemeye hazırdır.
- **Offsite'a şifreli `copy` ile tek okuma:** şu an iki ayrı `backup` çalışır
  (kaynak iki kez okunur); veri çok büyürse `restic copy` akışına geçilebilir.
- **Prometheus metrikleri:** `backup_last_success_timestamp_seconds` vb.
  (şu an webhook + status API var).
