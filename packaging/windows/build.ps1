# Build NecipDrive Sync Windows amd64 GUI binary + optional installer.
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$OutDir = Join-Path $Root "dist\windows"
$UiDir = Join-Path $Root "internal\desktop\ui"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$env:Path = [System.Environment]::GetEnvironmentVariable("Path","Machine") + ";" + [System.Environment]::GetEnvironmentVariable("Path","User")

Write-Host "Building desktop UI..."
Push-Location $UiDir
try {
  if (-not (Test-Path "node_modules")) {
    $webNm = Join-Path $Root "web\node_modules"
    if (Test-Path $webNm) {
      cmd /c "mklink /J `"$UiDir\node_modules`" `"$webNm`""
    } else {
      npm ci --no-audit --no-fund
      if ($LASTEXITCODE -ne 0) { npm install --no-audit --no-fund }
    }
  }
  npx vite build
  if ($LASTEXITCODE -ne 0) { throw "desktop UI build failed" }
} finally {
  Pop-Location
}

Write-Host "Building Windows GUI binary..."
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

Push-Location $Root
try {
  go mod tidy
  go build -ldflags "-s -w -H windowsgui" -o (Join-Path $OutDir "necipdrive-sync.exe") ./cmd/necipdrive-sync
  if ($LASTEXITCODE -ne 0) { throw "go build failed" }
  Write-Host "Built:" (Join-Path $OutDir "necipdrive-sync.exe")

  # Optional console debug binary
  go build -ldflags "-s -w" -o (Join-Path $OutDir "necipdrive-sync-console.exe") ./cmd/necipdrive-sync
} finally {
  Pop-Location
}

$iscc = @(
  "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
  "${env:ProgramFiles}\Inno Setup 6\ISCC.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1

if ($iscc) {
  & $iscc (Join-Path $PSScriptRoot "necipdrive-sync.iss")
} else {
  Write-Host "Inno Setup not found; skipped installer. Binary is ready in dist\windows."
}
