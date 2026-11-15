# TR Driver Android

## Release APK
`web/public/apps/TRDriver.apk` (+ `dist/android/TRDriver.apk`)  
Web: `/download/TRDriver.apk` · Sürüm: `GET /api/android/version`

Sideloading’de “Play Protect / doğrulanmadı” uyarısı Play Store dışı kurulumlarda normaldir; APK **release imzalı**dır.

## Uygulama içi güncelleme
1. `.\android\scripts\publish-apk.ps1 -VersionCode N -VersionName x.y.z`
2. `web/public/apps/TRDriver.apk` + `android-version.json` dosyalarını VPS mount’a kopyala (veya git push)
3. Telefon: uygulamayı aç → Güncelle diyaloğu (veya İşlemler → Güncellemeyi kontrol et)

`versionCode` her yayında artmalı; aynı release keystore ile imzalayın.

## Özellikler
- Üye ol / giriş / QR ile giriş
- Galeri + ek klasör yedek (foto, video, ses, tüm dosyalar)
- Rehber ve SMS yedeği (yalnız Wi‑Fi, açıklama diyaloğuyla izin, artımlı; sunucuda şifreli) — `docs/PERSONAL_BACKUP.md`
- LocalTel IP/SIP sesli arama (rehber eşleştirme, kilit ekranı tam ekran Aç/Reddet, Android Telecom) — `docs/LOCALTEL_CALLING.md`
- Video/müzik/resim önizleme
- Arka plan müzik + mini player
- Uygulama içi APK güncelleme
- Rehber/SMS için ayrı açma‑kapama anahtarları ve "sunucudaki kopyayı sil" düğmesi
