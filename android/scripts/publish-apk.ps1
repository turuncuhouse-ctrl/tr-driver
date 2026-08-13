# Publish signed TRDriver.apk + android-version.json into web/public/apps
param(
    [Parameter(Mandatory = $true)]
    [int]$VersionCode,
    [Parameter(Mandatory = $true)]
    [string]$VersionName,
    [string]$ReleaseNotes = "TR Driver güncellemesi",
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$androidDir = Join-Path $root "android"
$appsDir = Join-Path $root "web\public\apps"
$apkOut = Join-Path $androidDir "app\build\outputs\apk\release\app-release.apk"
$destApk = Join-Path $appsDir "TRDriver.apk"
$destJson = Join-Path $appsDir "android-version.json"
$distApk = Join-Path $root "dist\android\TRDriver.apk"

New-Item -ItemType Directory -Force -Path $appsDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path $distApk) | Out-Null

# Keep build.gradle version in sync when publishing.
$gradle = Join-Path $androidDir "app\build.gradle.kts"
$gradleText = Get-Content $gradle -Raw
$gradleText = [regex]::Replace($gradleText, 'versionCode\s*=\s*\d+', "versionCode = $VersionCode")
$gradleText = [regex]::Replace($gradleText, 'versionName\s*=\s*"[^"]+"', "versionName = `"$VersionName`"")
Set-Content -Path $gradle -Value $gradleText -NoNewline

if (-not $SkipBuild) {
    Push-Location $androidDir
    try {
        & .\gradlew.bat assembleRelease --no-daemon
        if ($LASTEXITCODE -ne 0) { throw "assembleRelease failed" }
    } finally {
        Pop-Location
    }
}

if (-not (Test-Path $apkOut)) {
    throw "APK not found: $apkOut"
}

Copy-Item $apkOut $destApk -Force
Copy-Item $apkOut $distApk -Force

$json = @{
    versionCode      = $VersionCode
    versionName      = $VersionName
    minSupportedCode = 1
    releaseNotes     = $ReleaseNotes
    apkPath          = "/download/TRDriver.apk"
} | ConvertTo-Json
Set-Content -Path $destJson -Value $json -Encoding UTF8

Write-Host "Published:"
Write-Host "  $destApk"
Write-Host "  $destJson"
Write-Host "  $distApk"
Write-Host "Next: copy web/public/apps/* to VPS (git push or scp). Redeploy only if Go/web code changed."
