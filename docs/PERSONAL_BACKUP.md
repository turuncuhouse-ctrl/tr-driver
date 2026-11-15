# Rehber ve SMS yedeği (kişisel sigorta yedeği)

Telefon kaybolduğunda / bozulduğunda kişileri ve SMS'leri geri yükleyebilmek için
Android istemci, rehberini ve mesajlarını **kendi sunucunuza** yedekler. Bu belge
sunucu API'sini, şifreleme modelini ve istemci davranışını anlatır.

Kapsam dışı: arama kaydı, medya (medya için bkz. [BACKUP.md](BACKUP.md) sunucu
yedekleri ve Android galeri yedeği), ofis dosyası düzenleme.

## Kurallar (istemci)

| Kural | Uygulama |
| --- | --- |
| Yalnız Wi‑Fi | `UploadNetworkGate` — mobil veri ile rehber/SMS yedeği yok |
| Daima arka planda | `TrKeepAliveService` (kalıcı FGS) + `GalleryBackupWorker` (WorkManager yedeği) aynı döngüyü paylaşır |
| Wi‑Fi gelince devam | Ağ callback'i + 15 dk watchdog + `WifiBackupWatcher` |
| Hız sınırı | Medya yükleme hız sınırı (`UploadBandwidthLimiter`) değişmedi; rehber/SMS küçük partiler hâlinde ve partiler arasında 1.2 sn beklemeyle gönderilir |
| Açık rıza | Anahtarlar varsayılan kapalı; izin öncesi açıklama diyaloğu, sonra sistem izin penceresi |
| PII loglanmaz | İstemci yalnızca hata sınıfını loglar; sunucu istek gövdesini hiç loglamaz |

İstemci rehberi `ContactsContract` üzerinden (ad, telefon, e‑posta, adres, kurum,
not, takma ad, doğum günü, web sitesi, yıldız), SMS'i `content://sms` üzerinden
(numara, metin, tarih, tür, okundu, thread, servis merkezi, konu) okur.

## Sunucu API'si

Tüm uç noktalar `Authorization: Bearer <cihaz-token>` (veya oturum çerezi) ister
ve **yalnızca çağıranın kendi kayıtlarını** döndürür.

| Yöntem | Yol | Açıklama |
| --- | --- | --- |
| `GET` | `/api/personal-backup/status` | Tür başına kayıt sayısı ve son eşitleme zamanı |
| `POST` | `/api/personal-backup/contacts` | Artımlı kişi gönderimi |
| `POST` | `/api/personal-backup/sms` | Artımlı SMS gönderimi |
| `GET` | `/api/personal-backup/{contacts,sms}` | Geri yükleme listesi (`since`, `after`, `limit`) |
| `DELETE` | `/api/personal-backup/{contacts,sms}` | Kullanıcının o türdeki tüm kayıtlarını siler |

### Artımlı gönderim

```json
{
  "deviceId": "Pixel 7",
  "items": [
    {
      "key": "QWxsZXJnaWVzLXVuZGluZ2Uta2V5MDE",
      "revision": 1718000000000,
      "hash": "9f2c…64 hex…",
      "data": { "displayName": "Ayşe", "phones": [{ "type": "2", "value": "+905551112233" }] }
    }
  ],
  "deleted": ["c2lsaW5lbi1rZXktMDAwMDAwMDAwMDAx"]
}
```

Yanıt:

```json
{ "inserted": 1, "updated": 0, "unchanged": 0, "stale": 0, "deleted": 2, "total": 184, "syncedAt": "2026-11-15T10:00:00Z" }
```

Kurallar:

- `key`: istemcinin ürettiği opak anahtar (base64url SHA‑256, 43 karakter).
  Sunucu bunu **saklamaz**; HMAC ile kör indekse çevirir.
- `hash`: içerik SHA‑256'sı. Aynı hash tekrar gelirse satır yazılmaz (`unchanged`).
- `revision`: kaydın son değişim zamanı (kişide `CONTACT_LAST_UPDATED_TIMESTAMP`,
  SMS'te tarih). Sunucudaki revizyon daha yeniyse istek `stale` sayılır ve **eski
  veri üzerine yazmaz**.
- `deleted`: yalnızca telefon tam tarama yaptıysa ve o kayıt gerçekten yoksa
  gönderilir (tarama yarım kaldıysa istemci hiçbir silme göndermez).
- Parti sınırları: tek istekte en fazla 500 kayıt / 2000 silme; kişi başına
  16 KiB, SMS başına 64 KiB, istek gövdesi 4 MiB.

### Geri yükleme

```bash
# İlk sayfa
curl -H "Authorization: Bearer $TOKEN" \
  "https://sunucu/api/personal-backup/contacts?limit=200"

# Devamı (nextAfter boşalana kadar)
curl -H "Authorization: Bearer $TOKEN" \
  "https://sunucu/api/personal-backup/contacts?limit=200&after=…"
```

`since` (RFC3339) yalnızca belirtilen zamandan sonra güncellenen kayıtları
döndürür; `nextAfter` boşsa liste bitti.

## Şifreleme ve gizlilik

- Her kayıt AES‑256‑GCM ile şifrelenir; nonce rastgele, şifreli metin kullanıcı
  kimliği + tür ile bağlanır (AAD). Yani bir satır başka bir hesaba kopyalanırsa
  **çözülemez**.
- Satır anahtarı `HMAC-SHA256(sunucu sırrı, tür || key)` — veritabanı dökümünde
  telefon numarası, isim, mesaj metni veya istemci anahtarı bulunmaz.
- Anahtar: `PERSONAL_BACKUP_KEY` (≥16 karakter). Boşsa `SESSION_SECRET`'ten
  türetilir; ayrı bir değer verirseniz oturum sırrını döndürmek eski kayıtları
  bozmaz. **Anahtarı kaybetmek yedeklerin okunamaması demektir.**
- Yazma uçlarında kullanıcı başına token bucket hız sınırı vardır (dakikada 240,
  burst 20) → `429` + `Retry-After`.
- Sunucu logları yalnızca yöntem + yol yazar; sorgu parametreleri ve gövde
  loglanmaz.

## Yasal not

SMS'e erişim (`READ_SMS`) Google Play'de kısıtlı bir izindir. Bu uygulama kendi
APK'nızı yan yükleyerek (sideload) kullanılmak üzere tasarlanmıştır; Play
Store'da yayınlayacaksanız izin beyanı gerekir. Kullanıcı yalnızca kendi
cihazındaki kendi verisini yedekler.
