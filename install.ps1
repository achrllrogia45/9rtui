$ErrorActionPreference = "Stop"

$Repo = if ($env:NRTUI_REPO) { $env:NRTUI_REPO } else { "achrllrogia45/9rtui" }
$Version = if ($env:NRTUI_VERSION) { $env:NRTUI_VERSION } else { "v0.1beta" }
$Branch = if ($env:NRTUI_BRANCH) { $env:NRTUI_BRANCH } else { "main" }
$HomeDir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $HOME }
$InstallDir = if ($env:NRTUI_INSTALL_DIR) { $env:NRTUI_INSTALL_DIR } else { Join-Path $HomeDir ".9rtui" }
$ApiBase = if ($env:NRTUI_API) { $env:NRTUI_API } else { "http://localhost:20128" }
$DbPath = if ($env:NRTUI_DB) { $env:NRTUI_DB } else { Join-Path $env:APPDATA "9router\db\data.sqlite" }
$CacheDir = if ($env:NRTUI_CACHE_DIR) { $env:NRTUI_CACHE_DIR } else { Join-Path $InstallDir "tmp" }
$SrcDir = Join-Path $CacheDir "src"

function Say($Message) { Write-Host $Message }
function Fail($Message) { throw $Message }

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".accounts") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".tui-logs\full-backups") | Out-Null
New-Item -ItemType Directory -Force -Path $CacheDir | Out-Null

$Arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($Arch) {
    "x64" { $GoArch = "amd64" }
    "arm64" { $GoArch = "arm64" }
    default { Fail "unsupported architecture: $Arch" }
}
$Asset = "9rtui-windows-$GoArch.exe"
$Exe = Join-Path $InstallDir "9rtui.exe"

function Sync-Scripts {
    $Zip = Join-Path $CacheDir "9rtui-$Branch.zip"
    $Url = "https://github.com/$Repo/archive/refs/heads/$Branch.zip"
    if (!(Test-Path $Zip) -or $env:NRTUI_REFRESH -eq "1") {
        Say "download scripts/source: $Url"
        Invoke-WebRequest -Uri $Url -OutFile $Zip
    }
    if (Test-Path $SrcDir) { Remove-Item -Recurse -Force $SrcDir }
    New-Item -ItemType Directory -Force -Path $SrcDir | Out-Null
    Expand-Archive -Force -Path $Zip -DestinationPath $CacheDir
    $Expanded = Get-ChildItem $CacheDir -Directory | Where-Object { $_.Name -like "9rtui-*" -and $_.FullName -ne $SrcDir } | Select-Object -First 1
    if (!$Expanded) { Fail "failed to locate extracted source" }
    Move-Item -Force $Expanded.FullName $SrcDir
    $Scripts = Join-Path $InstallDir "scripts"
    if (Test-Path $Scripts) { Remove-Item -Recurse -Force $Scripts }
    Copy-Item -Recurse (Join-Path $SrcDir "scripts") $Scripts
}

function Build-FromSource {
    if (!(Get-Command go -ErrorAction SilentlyContinue)) { Fail "go missing; install Go or use prebuilt release installer without NRTUI_BUILD_FROM_SOURCE=1" }
    Sync-Scripts
    Push-Location $SrcDir
    $BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    go build -trimpath -ldflags "-s -w -X main.version=$Version -X main.commit=source -X main.buildDate=$BuildDate" -o $Exe .
    Pop-Location
}

if ($env:NRTUI_BUILD_FROM_SOURCE -eq "1") {
    Say "build from source"
    Build-FromSource
} else {
    $Url = "https://github.com/$Repo/releases/download/$Version/$Asset"
    $Cached = Join-Path $CacheDir $Asset
    if (!(Test-Path $Cached) -or $env:NRTUI_REFRESH -eq "1") {
        Say "download binary: $Url"
        Invoke-WebRequest -Uri $Url -OutFile $Cached
    }
    Copy-Item -Force $Cached $Exe
    Sync-Scripts
}

$EnvPath = Join-Path $InstallDir ".env"
if (!(Test-Path $EnvPath)) { "WEB_PASS=" | Set-Content -Encoding UTF8 $EnvPath }

$IniPath = Join-Path $InstallDir "9rtui.ini"
if (!(Test-Path $IniPath)) {
@"
project_dir = .
db_path = $DbPath
log_dir = .\.tui-logs
api_base = $ApiBase
accounts_path = .\.accounts\
dev_mode = false
"@ | Set-Content -Encoding UTF8 $IniPath
}

$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
    $NewPath = if ([string]::IsNullOrWhiteSpace($UserPath)) { $InstallDir } else { "$UserPath;$InstallDir" }
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    Say "Added to user PATH. Restart terminal to use 9rtui globally."
}

Say "installed: $Exe"
Say "scripts:   $(Join-Path $InstallDir "scripts")"
Say "config:    $IniPath"
Say "run:       9rtui"
