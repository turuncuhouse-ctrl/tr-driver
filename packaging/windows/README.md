# NecipDrive Sync (Windows)

Google Drive benzeri tray uygulaması.

## Özellikler
- Konsolsuz arka plan süreci (`-H windowsgui`)
- Sistem tepsisi ikonu (sol tık: durum, sağ tık: menü)
- WebView2 ayarlar/durum penceresi
- Pencereyi kapatmak senkronizasyonu durdurmaz; yalnızca gizler
- Yalnızca “Çıkış” ile kapanır
- Single-instance: ikinci açılış mevcut pencereyi öne getirir
- Windows ile başlatma
- Loglar: `%LOCALAPPDATA%\NecipDrive\logs\sync.log`

## Gereksinim
- Windows 10/11
- Microsoft Edge WebView2 Runtime (çoğu sistemde yüklü)

## Derleme

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\build.ps1
```

Çıktı:
- `dist\windows\necipdrive-sync.exe` (GUI)
- `dist\windows\necipdrive-sync-console.exe` (debug)
- Inno Setup varsa `dist\windows\NecipDriveSyncSetup.exe`

## Kullanım
Kurulumdan sonra Start Menu’den açın. Uygulama tray’e düşer.
İlk kurulumda Ayarlar’dan giriş yapıp senkron klasörü seçin.
