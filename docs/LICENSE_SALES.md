# TR Driver lisans satışı ve kırılmaya karşı model

## Akış (talep → yanıt)

```
Müşteri Admin                         Siz (satıcı)
─────────────                         ────────────
1) Paket seç
2) "Talep kodu üret"  ──TRDR1...──►  3) Admin (VENDOR) veya CLI
                                      4) Yanıt TRD1... üret
5) TRD1 yapıştır ◄──────────────────  5) Müşteriye ilet
6) Etkinleştir (instance bağlanır)
```

- Talep (`TRDR1`) kurulumun **instanceId**’sini taşır.
- Yanıt (`TRD1`) Ed25519 ile imzalanır ve **aynı instanceId**’ye kilitlenir.
- Başka sunucuya aynı anahtar yapıştırılmaz.

## Satıcı kurulumu (sizin VPS)

```
LICENSE_VENDOR_MODE=true
LICENSE_PRIVATE_KEY=<seed-b64>   # asla GitHub’a koyma
LICENSE_PUBLIC_KEY=<pub-b64>     # müşteri sunucularında da aynı public
```

Admin’de “Satıcı: yanıt lisansı üret” görünür.

CLI:

```powershell
$env:LICENSE_PRIVATE_KEY="..."
go run ./cmd/trdriver-licensegen -request "TRDR1...." -years 1 -customer "Firma A"
```

## Müşteri sunucusu

```
LICENSE_PUBLIC_KEY=<aynı-public-b64>
# PRIVATE key YOK — imza atamaz, sadece doğrular
```

## Kırılma / korsana karşı gerçekçi sınır

Açık kaynak yazılımda lisans **mutlak** kırılamaz yapılamaz (kaynak patch’lenebilir). Yaptıklarımız:

| Önlem | Etki |
|-------|------|
| Private key sadece sizde | Sahte anahtar üretmek zor (anahtar sızmazsa) |
| Instance bağlama | Başka kurulumda aynı key geçersiz |
| Talep checksum + süre | Eski/değiştirilmiş talep reddi |
| Device token ≠ admin | Sync token ile lisans üretilemez |
| Destek / güncelleme koşulu | Kırık kopyaya destek yok (sözleşme) |

**Yayın mesajı:** MIT kod + ticari koltuk lisansı. Kırılmış kurulum destek/güncelleme almaz.

## Ödeme

Manuel (havale) → talep kodu → siz yanıt üretirsiniz. Otomatik ödeme sonra eklenebilir.
