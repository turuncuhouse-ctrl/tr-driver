# TR Driver — Windows

## WebDAV ağ sürücüsü (hızlı Explorer erişimi)

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\mount-drive.ps1 -ServerUrl "https://drive.ornek.com" -DriveLetter Z
```

- Yol: `/dav` (e-posta + şifre)
- Not: Hesapta e-posta 2FA açıksa Basic auth çalışmaz; web oturumu veya 2FA kapalı hesap kullanın
- Web sitesinde sürücü değişiklikleri ~4 sn içinde görünür

## TR Driver Sync (çift yönlü klasör senkronu)

Google Drive benzeri tray uygulaması: seçilen Windows klasörü ↔ sunucu.

### Derleme / paket

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\build.ps1
```

Çıktı:
- `dist\windows\necipdrive-sync.exe`
- Inno Setup yüklüyse: `dist\windows\TRDriverSyncSetup.exe`

### Kullanım
1. Kurulumda veya ilk açılışta sunucu URL, e-posta, şifre
2. Yerel klasör ekle
3. Tray’den Başlat — yerel değişiklikler ~250ms debounce + uzak değişiklikler ~5 sn poll

### Gereksinim
- Windows 10/11
- Edge WebView2 Runtime
