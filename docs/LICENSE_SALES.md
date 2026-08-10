# TR Driver lisans satışı ve kırılmaya karşı model

## Akış (talep → yanıt)

```
Müşteri Admin                         Satıcı (yerel araç)
─────────────                         ──────────────────
1) Paket seç
2) "Talep kodu üret"  ──TRDR1...──►  3) Talep kodundan yanıt üret
                                      4) Yanıt TRD1... 
5) TRD1 yapıştır ◄──────────────────  5) Müşteriye ilet
6) Etkinleştir (instance bağlanır)
```

- Talep (`TRDR1`) kurulumun **instanceId**’sini taşır.
- Yanıt (`TRD1`) Ed25519 ile imzalanır ve **aynı instanceId**’ye kilitlenir.
- Başka sunucuya aynı anahtar yapıştırılmaz.

## Müşteri sunucusu

```
LICENSE_PUBLIC_KEY=<satıcının-public-b64>
# PRIVATE key YOK — imza atamaz, sadece doğrular
```

Admin’den talep kodu üretin → satıcıya gönderin → gelen `TRD1` yanıtını etkinleştirin.

## Satıcı

Yanıt üretimi **yerel / offline araç** ile yapılır. Private key yalnızca satıcı makinesinde kalır; public README veya müşteri VPS’ine konmaz.
İsteğe bağlı olarak kendi satıcı VPS’inizde `LICENSE_VENDOR_MODE` ile Admin üzerinden de üretilebilir — bu kurulum dokümanı ayrıdır ve public satış anlatımında yer almaz.

## Kırılma / korsana karşı gerçekçi sınır

Açık kaynak yazılımda lisans **mutlak** kırılamaz yapılamaz (kaynak patch’lenebilir). Yaptıklarımız:

| Önlem | Etki |
|-------|------|
| Private key sadece satıcıda | Sahte anahtar üretmek zor (anahtar sızmazsa) |
| Instance bağlama | Başka kurulumda aynı key geçersiz |
| Talep checksum + süre | Eski/değiştirilmiş talep reddi |
| Device token ≠ admin | Sync token ile lisans üretilemez |
| Destek / güncelleme koşulu | Kırık kopyaya destek yok (sözleşme) |

**Yayın mesajı:** MIT kod + ticari koltuk lisansı. Kırılmış kurulum destek/güncelleme almaz.

## Ödeme

Manuel (havale) → talep kodu → satıcı yanıt üretir. Otomatik ödeme sonra eklenebilir.

## Fiyatlar (yıllık)

| Paket | Kullanıcı | Fiyat |
|-------|-----------|-------|
| personal | 1 | Ücretsiz |
| small | 2–20 | 499 TL |
| medium | 21–100 | 1499 TL |
| unlimited | 1000+ | 2999 TL |
