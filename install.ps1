# 9rtui installer (dummy repo placeholder)
# Usage:
# powershell -ExecutionPolicy Bypass -NoProfile -Command "irm https://raw.githubusercontent.com/OWNER/9rtui/main/install.ps1 | iex"

$ErrorActionPreference = "Stop"

$Repo = if ($env:NINETUI_REPO) { $env:NINETUI_REPO } else { "OWNER/9rtui" }
$HomeDir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $HOME }
$InstallDir = if ($env:NINETUI_INSTALL_DIR) { $env:NINETUI_INSTALL_DIR } else { Join-Path $HomeDir ".9rtui" }
$ApiBase = if ($env:NINETUI_API) { $env:NINETUI_API } else { "http://localhost:20128" }
$DbPath = if ($env:NINETUI_DB) { $env:NINETUI_DB } else { Join-Path $HomeDir ".9router\db\data.sqlite" }

function Say($Message) { Write-Host $Message }
function Fail($Message) { throw $Message }

$Arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($Arch) {
    "x64" { $GoArch = "amd64" }
    "arm64" { $GoArch = "arm64" }
    default { Fail "unsupported architecture: $Arch" }
}

$Asset = "9rtui-windows-$GoArch.exe"
$ExePath = Join-Path $InstallDir "9rtui.exe"

Say "Installing 9rtui"
Say "  repo:        $Repo"
Say "  asset:       $Asset"
Say "  install dir: $InstallDir"

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".accounts") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".tui-logs") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".tui-logs\full-backups") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".dev") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".reports") | Out-Null

$EnvPath = Join-Path $InstallDir ".env"
if (!(Test-Path $EnvPath)) {
    "WEB_PASS=" | Set-Content -Encoding UTF8 $EnvPath
}

$IniPath = Join-Path $InstallDir "9rtui.ini"
if (!(Test-Path $IniPath)) {
@"
# 9rtui settings
[paths]
project_dir = $InstallDir
db_path = $DbPath
log_dir = $InstallDir\.tui-logs
api_base = $ApiBase
accounts_path = $InstallDir\.accounts\
dev_mode = false
"@ | Set-Content -Encoding UTF8 $IniPath
}

$ReleaseUrl = "https://api.github.com/repos/$Repo/releases/latest"
$Release = Invoke-RestMethod -Uri $ReleaseUrl
$AssetObj = $Release.assets | Where-Object { $_.name -eq $Asset } | Select-Object -First 1

if (!$AssetObj) {
    Fail "release asset not found: $Asset (dummy repo? set NINETUI_REPO=owner/repo)"
}

$Tmp = "$ExePath.tmp"
Invoke-WebRequest -Uri $AssetObj.browser_download_url -OutFile $Tmp

if (Test-Path $ExePath) {
    Move-Item -Force $ExePath "$ExePath.previous"
}
Move-Item -Force $Tmp $ExePath

$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
    $NewPath = if ([string]::IsNullOrWhiteSpace($UserPath)) { $InstallDir } else { "$UserPath;$InstallDir" }
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    Say "Added to user PATH. Restart terminal to use 9rtui globally."
}

Say "Installed: $ExePath"
Say "Config:    $IniPath"
Say "Run:       9rtui"
