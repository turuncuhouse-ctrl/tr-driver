# NecipDrive / TR Driver — Windows

## Birincil: WebDAV ağ sürücüsü

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\mount-drive.ps1 -ServerUrl "https://drive.ornek.com" -DriveLetter Z
```

Sunucu yolu: `/dav` (hesap e-posta + şifre, Basic auth). HTTPS önerilir.

## İkincil: Tray sync

Google Drive benzeri tray uygulaması (isteğe bağlı). WebDAV mount birincil Windows erişim yoludur.

### Özellikler (sync)
- Konsolsuz arka plan süreci (`-H windowsgui`)
- Sistem tepsisi ikonu
- WebView2 ayarlar/durum penceresi
- Single-instance, Windows ile başlatma
- Loglar: `%LOCALAPPDATA%\NecipDrive\logs\sync.log`

### Gereksinim
- Windows 10/11
- Microsoft Edge WebView2 Runtime

### Derleme

```powershell
powershell -ExecutionPolicy Bypass -File packaging\windows\build.ps1
```
