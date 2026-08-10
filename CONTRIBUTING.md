# Contributing to TR Driver

Teşekkürler — amaç Google Drive ücretsiz alternatifini büyütmek.

## Geliştirme

1. Go 1.23+, Node 20+, PostgreSQL 16
2. `SESSION_SECRET` ve `DATABASE_URL` ayarlayın
3. `cd web && npm ci && npm run build`
4. `go test ./...`
5. `go run ./cmd/server`

## Kurallar

- Küçük, odaklı PR’lar
- Güvenlik / auth / shares değişikliklerinde test veya net doğrulama adımları
- Secret / gerçek parola commit etmeyin
- UI metinlerinde ürün adı **TR Driver**

## Lisans anahtarları

Satış anahtarları üretmek için `cmd/trdriver-licensegen` kullanın. Varsayılan RFC test anahtarıyla imzalanmış anahtarları **satmayın**; kendi `LICENSE_PRIVATE_KEY` / `LICENSE_PUBLIC_KEY` çiftinizi oluşturun.

## Sorun bildirimi

- Repro adımları, beklenen / görülen
- Versiyon (`/healthz`), OS, deploy şekli (Docker/VPS)
- Güvenlik açıkları için `SECURITY.md` sürecini izleyin
