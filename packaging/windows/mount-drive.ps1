# TR Driver — Windows ağ sürücüsü (WebDAV)
#
# Örnek:
#   powershell -ExecutionPolicy Bypass -File packaging\windows\mount-drive.ps1 -ServerUrl "https://drive.ornek.com" -DriveLetter "Z"
#
# Not: Windows WebDAV Basic auth için HTTPS önerilir.
# Tray sync istemcisi ikincil bir seçenektir; birincil yol WebDAV + net use'dır.

param(
  [Parameter(Mandatory = $true)]
  [string]$ServerUrl,

  [string]$DriveLetter = "Z",

  [string]$Username = ""
)

$ErrorActionPreference = "Stop"
$base = $ServerUrl.TrimEnd("/")
$dav = "$base/dav"

# Windows bazen http WebDAV'ı engeller; HTTPS tercih edin.
Write-Host "WebDAV yolu: $dav"
Write-Host "Windows ağ sürücüsü eşleştiriliyor ($DriveLetter`:) ..."

if (-not $Username) {
  $Username = Read-Host "TR Driver kullanıcı e-postası"
}

$secure = Read-Host "Şifre" -AsSecureString
$cred = New-Object System.Management.Automation.PSCredential ($Username, $secure)

# Mevcut eşleşmeyi temizle (hata yok sayılır)
cmd /c "net use ${DriveLetter}: /delete /y" 2>$null | Out-Null

# Credential'ı Windows'a kaydet (WebDAV Basic)
cmdkey /generic:"$dav" /user:"$Username" /pass:(
  [Runtime.InteropServices.Marshal]::PtrToStringAuto(
    [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
  )
) | Out-Null

net use "${DriveLetter}:" $dav /user:$Username $cred.GetNetworkCredential().Password /persistent:yes
if ($LASTEXITCODE -ne 0) {
  Write-Error "net use başarısız. HTTPS kullandığınızdan ve /dav yolunun açıldığından emin olun."
}

Write-Host "Tamam: $DriveLetter`: -> $dav"
Write-Host "Explorer'da Bu Bilgisayar altında görünecek."
