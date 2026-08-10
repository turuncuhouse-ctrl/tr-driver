# TR Driver — lisans satışı (üretim anahtarları)

Satış öncesi **kendi** Ed25519 çiftinizi üretin. Repodaki varsayılan anahtar yalnızca geliştirme içindir.

## 1) Anahtar üret (tek sefer)

Bilgisayarınızda:

```powershell
cd C:\Users\necip\Documents\necipdrive
go run ./scripts/gen_license_keypair.go
```

Çıktı:

- `LICENSE_PUBLIC_KEY` → müşteri sunucularına / kendi VPS’inize (doğrulama)
- `LICENSE_PRIVATE_KEY` → **sadece sizin güvenli yeriniz** (satış makinesi / parola yöneticisi). Asla git’e koymayın.

## 2) Sunucuya public key

Portainer / compose env:

```
LICENSE_PUBLIC_KEY=<PUBLIC_B64>
```

Kod içindeki default public key’i de aynı değere güncelleyebilirsiniz (`internal/license/crypto.go`) ki env unutulsa bile sizin anahtarınız çalışsın.

## 3) Müşteriye anahtar sat

```powershell
$env:LICENSE_PRIVATE_KEY="<PRIVATE_SEED_B64>"
go run ./cmd/trdriver-licensegen -tier small -years 1 -customer "Musteri Adi"
```

Tier: `personal` (1) · `small` (10) · `medium` (50) · `unlimited` (∞)

Fiyatlar (yıllık): 99 / 499 / 1499 / 2999 TL.

Müşteri Admin → Lisans alanına `TRD1....` yapıştırır.

## 4) Ödeme

Şimdilik manuel: Havale/EFT veya iyzico linki → siz anahtar üretip e-posta ile gönderin. Otomatik ödeme gateway ayrı iş.
