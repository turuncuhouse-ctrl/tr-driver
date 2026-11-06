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
6. Ters vekil arkasında `TRUSTED_PROXY_CIDRS` ile yalnızca güvendiğiniz vekil ağlarını tanımlayın; boşsa `X-Forwarded-*` yok sayılır
7. TOTP secret’ları `SESSION_SECRET` ile şifrelenir; secret rotasyonu TOTP’u geçersiz kılar, kullanıcıların yeniden kurulum yapması gerekir

## Bilinen model kararları

- Device bearer token sync API’yi kullanır; **admin API’ye kapalıdır**
- Public share dosya/klasör erişimi paylaşım kökünün alt ağacı ile sınırlıdır
- Ücretsiz sürüm: kullanıcı sayısı sınırı yok; kota Admin’den ayarlanır
- CSRF token’ları oturum cookie’sine HMAC ile bağlıdır; oturum değişince token da değişir
- Paylaşım parolaları Argon2id ile saklanır; eski SHA-256 hash’leri başarılı ilk açılışta yükseltilir
- Paylaşım açma cookie’si düz metin parola değil, link+hash’e bağlı HMAC kanıtı taşır
- Android oturum/yapılandırma tercihleri Keystore destekli EncryptedSharedPreferences’ta tutulur; `allowBackup=false`
- Cihaz token’ları varsayılan olarak süresizdir (`DEVICE_TOKEN_TTL=0`); cihaz iptali, çıkış veya şifre sıfırlama ile anında geçersiz olur
- WebDAV için parola yerine cihaz token’ı kullanılabilir; 2FA açıksa önerilen yöntem budur
- View-only paylaşım bağlantıları dosya indirmeye izin vermez
