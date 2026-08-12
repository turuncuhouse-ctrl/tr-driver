# Security Policy — TR Driver

## Desteklenen sürümler

Aktif geliştirme: `main` / son tagged release.

## Raporlama

Güvenlik bulgularını **public issue açmadan** proje sahibine iletin (GitHub Security Advisory veya private iletişim).

Lütfen şunları ekleyin: etki, repro, etkilenen endpointler, önerilen düzeltme.

## Hardening checklist (operatör)

1. Güçlü unique `SESSION_SECRET` ve `SHARE_PASSWORD_SALT` (production’da ≥16, placeholder yok)
2. Compose/env içinde secret’ları asla git’e koyma; eski örnek secret’ları rotate et
3. HTTPS ters vekil (NPM/Caddy/Nginx) arkasında çalıştır
4. `ALLOW_REGISTRATION=false` ile davetsiz kaydı kapat (isteğe bağlı)
5. Cihaz (sync) token’larını sızdırma; admin işlemleri yalnızca tarayıcı oturumu

## Bilinen model kararları

- Device bearer token sync API’yi kullanır; **admin API’ye kapalıdır**
- Public share dosya/klasör erişimi paylaşım kökünün alt ağacı ile sınırlıdır
- Ücretsiz sürüm: kullanıcı sayısı sınırı yok; kota Admin’den ayarlanır
