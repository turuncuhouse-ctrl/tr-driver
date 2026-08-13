# Android APK distribution

Place the signed release APK here as `TRDriver.apk`.

Also keep `android-version.json` next to it so clients can discover updates without rebuilding the whole site:

```json
{
  "versionCode": 9,
  "versionName": "0.7.1",
  "minSupportedCode": 1,
  "releaseNotes": "Kısa not",
  "apkPath": "/download/TRDriver.apk"
}
```

## Serving

- APK: `/download/TRDriver.apk` and `/apps/TRDriver.apk`
- Version: `GET /api/android/version`

## Publish a new APK (no full Redeploy)

On your PC:

```powershell
.\android\scripts\publish-apk.ps1 -VersionCode 10 -VersionName 0.7.2 -ReleaseNotes "Düzeltmeler"
```

Then copy to the VPS mount (examples):

```text
web/public/apps/TRDriver.apk
web/public/apps/android-version.json
```

Because Portainer mounts `/mnt/1tb_disk/necipdrive` as `/app`, overwriting these files is enough. Redeploy is only needed when server/web code changes.

Phone users: open TR Driver → soft update dialog (or İşlemler → Güncellemeyi kontrol et).
