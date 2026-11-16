# TR Driver — Ortak Paylaşım ve torrent indirme

0.14.0 ile gelen **Ortak Paylaşım**, sunucudaki tek bir klasörün (`SHARED_DIR`,
Docker'da `/data/paylasim`) izin verilen kullanıcılarla paylaşılmasıdır. Kişisel
drive'dan ayrıdır: dosyalar diskte olduğu gibi durur, kullanıcı kotasına sayılmaz
ve kimin neyi görebileceğine yalnızca yönetici karar verir.

## 1. Kurulum / yükseltme

Depodaki Compose dosyaları gerekli volume, port ve ortam değişkenlerini içerir.
**Mevcut bir kurulumu yükseltiyorsanız** stack'inizi yeni Compose dosyasıyla
güncellemeden önce klasörü oluşturun:

```bash
mkdir -p /mnt/1tb_disk/necipdrive/paylasim
# Dockerfile ile derlenen imaj uid 1000 (appuser) olarak çalışır:
chown 1000:1000 /mnt/1tb_disk/necipdrive/paylasim
```

- Volume eklenmezse klasör konteynerin içinde oluşur ve **konteyner yeniden
  yaratıldığında dosyalar kaybolur**. `/data/paylasim` mutlaka kalıcı bir
  dizine bağlı olmalıdır.
- Klasör yazılamıyorsa sunucu yine açılır; yalnızca Ortak Paylaşım kapalı kalır
  ve logda `shared space disabled: SHARED_DIR=… is not usable` satırı görünür.
- `SHARED_DIR` ile `DATA_DIR` aynı ya da iç içe olamaz; sunucu bu durumda
  başlamayı reddeder.
- Klasörün kökünde sunucunun kullandığı gizli bir `.trdriver/` dizini bulunur;
  silmeyin.

> **Yedekleme:** yerleşik sunucu yedeği (`docs/BACKUP.md`) yalnızca veritabanını
> ve `DATA_DIR`'i alır. **Ortak Paylaşım klasörü yedeğe dahil değildir.** İzinler,
> gruplar ve bağlantı kayıtları veritabanıyla birlikte yedeklenir, dosyaların
> kendisi yedeklenmez. Buradaki dosyalar sizin için önemliyse klasörü ayrıca
> yedekleyin.

## 2. Ortam değişkenleri

| Değişken | Varsayılan | Açıklama |
|----------|-----------|----------|
| `SHARED_DIR` | `DATA_DIR`'in yanındaki `paylasim` | Ortak Paylaşım klasörü. |
| `SHARED_QUOTA_BYTES` | `0` | Alanın toplam boyut sınırı. `0` = sınır yok, disk dolana kadar. |
| `SHARED_QUOTA_WARN_PERCENT` | `90` | Uyarı eşiği (1–99). |
| `SHARED_LINK_MAX_TTL` | `720h` | Herkese açık bağlantıların en uzun geçerlilik süresi. |
| `TORRENT_ENABLED` | `true` | `false` ise torrent istemcisi hiç başlamaz. |
| `TORRENT_LISTEN_PORT` | `42069` | Eş bağlantı portu (TCP + UDP). |
| `TORRENT_DOWNLOAD_LIMIT_KBPS` | `0` | İndirme hız sınırı, KB/sn. `0` = sınırsız. |
| `TORRENT_UPLOAD_LIMIT_KBPS` | `0` | Gönderme hız sınırı, KB/sn. `0` = sınırsız. |
| `TORRENT_SEED_AFTER_COMPLETE` | `false` | `true` ise tamamlanan torrent paylaşılmaya devam eder. |
| `TORRENT_DISABLE_DHT` | `false` | `true` ise DHT kapatılır, yalnızca tracker kullanılır. |

Kota, uyarı eşiği ve torrent hız sınırları yönetici panelinden de değiştirilebilir;
panelden kaydedilen değer ortam değişkeninin önüne geçer.

Parça doğrulama bilgisi `DATA_DIR/torrent-state` altında saklanır (Ortak
Paylaşım klasörünün dışında); böylece sunucu yeniden başladığında indirilen
parçalar tekrar indirilmez. Bu dizin `DATA_DIR` volume'u ile birlikte yedeklenir.

## 3. İzinler

Varsayılan olarak **hiçbir kullanıcı** Ortak Paylaşım'ı göremez. Yönetici,
**Yönetim → Ortak Paylaşım** bölümünden klasör bazında izin verir:

- **Okuma:** klasörü ve altındakileri listeler, dosya indirir.
- **Yazma:** ayrıca yükler, klasör oluşturur, yeniden adlandırır, siler ve
  herkese açık bağlantı oluşturur.

İzin bir kullanıcıya ya da bir gruba verilebilir; alt klasörler üst klasörün
iznini devralır. Bir kullanıcı yalnızca izinli olduğu klasörleri ve onlara
giden yolu görür, diğer klasörlerin adını dahi göremez. Yöneticiler her şeye
erişir. Bir klasör silindiğinde ona verilmiş izinler de silinir ve o yola ait
bağlantılar iptal edilir.

Yönetim uçları (`/api/admin/shared/…`, `/api/admin/torrents`) depodaki diğer
yönetim uçları gibi yalnızca tarayıcı oturumuyla çalışır; cihaz (Android)
token'ıyla çağrılamaz.

## 4. Herkese açık bağlantılar

Yazma izni olan kullanıcı bir dosya ya da klasör için `/p/<token>` biçiminde
bağlantı oluşturabilir. Alanın kökü bağlantıyla paylaşılamaz.

- Geçerlilik süresi boş bırakılırsa 7 gündür; `SHARED_LINK_MAX_TTL` aşılamaz.
- İsteğe bağlı parola (en az 4 karakter) ve indirme sayısı sınırı verilebilir.
- Bağlantı adresi **yalnızca oluşturulduğu anda** gösterilir; sunucu token'ı
  saklamaz, sonradan yeniden görüntülenemez. Kaybedilirse yeni bağlantı
  oluşturun.
- Bağlantıyı oluşturan kişi izni kaybederse bağlantı da çalışmaz olur.
- Token'lar ve parolalar sunucu loglarına yazılmaz (`/p/[redacted]`).
- İndirilen dosyalar her zaman ek (attachment) olarak gönderilir; tarayıcıda
  sunucu alan adı altında HTML/SVG çalıştırılamaz.

## 5. Kota ve uyarılar

`SHARED_QUOTA_BYTES` ayarlıysa alan bu boyutu geçemez; ayarlı değilse sınır
diskteki boş alandır (sunucu her durumda diskte küçük bir pay bırakır). Doluluk
uyarı eşiğini geçtiğinde ve alan dolduğunda yöneticilere e-posta gönderilir
(posta ayarlıysa); aynı seviye için tekrar tekrar posta atılmaz. Alan doluyken
yükleme `507` ile reddedilir.

## 6. Torrent indirme (yalnızca yönetici)

**Yönetim → Torrent indirme** bölümünden magnet bağlantısı ya da `.torrent`
dosyası (en çok 8 MB) eklenir. İndirilenler Ortak Paylaşım'ın köküne iner;
tamamlanmamış dosyalar `.part` uzantısıyla durur, bitince asıl adını alır.
Kimlerin göreceğini yine klasör izinleri belirler.

- İndirme, duraklatma, sürdürme, kaldırma (dosyalarla ya da dosyasız) ve hız
  sınırı desteklenir. Sunucu yeniden başladığında indirmeler kaldığı yerden
  devam eder.
- İndirme süren bir klasör Ortak Paylaşım arayüzünden silinemez ya da yeniden
  adlandırılamaz (`409`).
- Torrent alan kotasına tabidir; yer kalmazsa indirme durur.
- Varsayılan olarak tamamlanan torrent paylaşılmaz
  (`TORRENT_SEED_AFTER_COMPLETE=false`). İndirme sırasında parçalar diğer eşlere
  gönderilir; bunu `TORRENT_UPLOAD_LIMIT_KBPS` ile sınırlayabilirsiniz.

### Torrent kontrolleri (yönetim paneli)

- **Hız sınırları:** Genel sınırların (`TORRENT_*_LIMIT_KBPS`) yanında her
  torrent'e ayrı indirme/gönderme sınırı (KB/sn) verilir; parça yazma/okuma
  bu sınırla akar, parça doğrulama beklemez. `0` = sınırsız.
- **Seed oranı / süresi sınırı:** Torrent başına oran (ör. `8` = yüklenen,
  indirilenin 8 katı) ve süre (sn) sınırı; `0` = kapalı. Sınır dolunca seed
  durur ve sunucu yeniden başlayınca kendiliğinden başlamaz — panelden
  "Seed başlat" ile yeniden açılır. "Seed'i durdur" ile elle durdurulur.
- **Dosya seçimi:** Ayrıntı çekmecesindeki dosya listesinden bir dosya
  "Atla" yapılabilir; o dosya indirilmez, kalan dosyalar bitince torrent
  tamamlanmış sayılır. "Yüksek" öncelik ilk indirilir.
- **Kaldır (veriyi silmeden):** Torrent listeden çıkar ama dosyalar yerinde
  kalır. Aynı magnet bağlantısı veya `.torrent` dosyası tekrar eklenince mevcut
  parçalar tek tek doğrulanır ve indirme kaldığı yerden sürer — bozuk parçalar
  algılanıp yeniden indirilir. "Veriyi de sil" seçeneği dosyaları da siler.
- **Duraklat / sürdür:** Duraklatma beklemeye alır; sürdürmeden önce dosyalar
  yeniden doğrulanır. Tamamlanan ve paylaşılan torrent "Seed" olarak görünür.

### Port

Compose dosyaları `TORRENT_LISTEN_PORT` (varsayılan 42069) portunu TCP ve UDP
olarak yayınlar. Gelen eş bağlantıları için bu portun güvenlik duvarında ve
varsa modem/yönlendiricide de açık olması gerekir; kapalıysa indirme yine çalışır
ama daha az eşe ulaşır. Port, konteyner içinde ve dışında **aynı numara**
olmalıdır. Portu dışarı açmak istemiyorsanız Compose'daki iki `ports` satırını
kaldırın; torrent'i hiç kullanmayacaksanız `TORRENT_ENABLED=false` verin.

### Bilinen sınırlar

- HTTP tracker bağlantıları sunucunun kendi ağına (loopback, özel ağ adresleri)
  yönlendirilemez. Bu koruma UDP tracker ve DHT trafiğini kapsamaz; bunlar
  doğrudan torrent kütüphanesi tarafından yönetilir.
- Torrent ile yalnızca dağıtma hakkına sahip olduğunuz içerikleri indirin;
  sorumluluk sunucu yöneticisindedir.

## 7. Android

Android uygulamasında **Diğer menüsü → Ortak Paylaşım** ekranı izinli klasörleri listeler;
dosya indirip açar, yazma izni varsa dosya yükler, klasör oluşturur, yeniden
adlandırır ve siler. Bağlantı oluşturma, izin yönetimi ve torrent yalnızca web
arayüzündedir.
