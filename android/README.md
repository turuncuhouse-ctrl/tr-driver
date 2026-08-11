# TR Driver Android (native lite)

Giriş, klasör gezinme, yükleme, indirme, klasör oluşturma ve silme.

## Gereksinimler
- JDK 17+
- Android SDK 34

`gradle.properties` içinde Windows TLS intercept için:
`org.gradle.jvmargs=... -Djavax.net.ssl.trustStoreType=Windows-ROOT`

## Derleme

```powershell
cd android
.\gradlew.bat assembleDebug
```

APK:

- `android/app/build/outputs/apk/debug/app-debug.apk`
- Kopya: `dist/android/TRDriver-debug.apk`

Telefona USB veya dosya paylaşımı ile kopyalayıp kurun (“Bilinmeyen uygulamalar” izni).

Otomatik galeri yedekleme: uygulamada **Otomatik foto/video yedekle** açın.
Dosyalar sunucuda `TR Photos / yıl / ay` altına gider. Wi‑Fi / mobil veri tercihi ayardan seçilir.
