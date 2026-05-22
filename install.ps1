$ErrorActionPreference = "Stop"

$Repo = if ($env:NRTUI_REPO) { $env:NRTUI_REPO } else { "achrllrogia45/9rtui" }
$Branch = if ($env:NRTUI_BRANCH) { $env:NRTUI_BRANCH } else { "main" }
$HomeDir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $HOME }
$InstallDir = if ($env:NRTUI_INSTALL_DIR) { $env:NRTUI_INSTALL_DIR } else { Join-Path $HomeDir ".9rtui" }
$ApiBase = if ($env:NRTUI_API) { $env:NRTUI_API } else { "http://localhost:20128" }
$DbPath = if ($env:NRTUI_DB) { $env:NRTUI_DB } else { Join-Path $env:APPDATA "9router\db\data.sqlite" }
$CacheDir = if ($env:NRTUI_CACHE_DIR) { $env:NRTUI_CACHE_DIR } else { Join-Path $InstallDir "tmp" }
$SrcDir = Join-Path $CacheDir "src"

function Say($Message) { Write-Host $Message }
function Fail($Message) { throw $Message }
function Need($Name) { if (!(Get-Command $Name -ErrorAction SilentlyContinue)) { Fail "missing dependency: $Name" } }

Need go

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".accounts") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".tui-logs\full-backups") | Out-Null
New-Item -ItemType Directory -Force -Path $CacheDir | Out-Null

$Zip = Join-Path $CacheDir "9rtui-$Branch.zip"
$Url = "https://github.com/$Repo/archive/refs/heads/$Branch.zip"
if (!(Test-Path $Zip) -or $env:NRTUI_REFRESH -eq "1") {
    Say "download: $Url"
    Invoke-WebRequest -Uri $Url -OutFile $Zip
} else {
    Say "using cached source archive: $Zip"
}

if (Test-Path $SrcDir) { Remove-Item -Recurse -Force $SrcDir }
New-Item -ItemType Directory -Force -Path $SrcDir | Out-Null
Expand-Archive -Force -Path $Zip -DestinationPath $CacheDir
$Expanded = Get-ChildItem $CacheDir -Directory | Where-Object { $_.Name -like "9rtui-*" -and $_.FullName -ne $SrcDir } | Select-Object -First 1
if (!$Expanded) { Fail "failed to locate extracted source" }
Move-Item -Force $Expanded.FullName $SrcDir

Push-Location $SrcDir
$Exe = Join-Path $InstallDir "9rtui.exe"
$BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
Say "build: $Exe"
go build -trimpath -ldflags "-s -w -X main.version=$Branch -X main.commit=source -X main.buildDate=$BuildDate" -o $Exe .
Pop-Location

Say "sync scripts"
$Scripts = Join-Path $InstallDir "scripts"
if (Test-Path $Scripts) { Remove-Item -Recurse -Force $Scripts }
Copy-Item -Recurse (Join-Path $SrcDir "scripts") $Scripts

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
Say "scripts:   $Scripts"
Say "config:    $IniPath"
Say "run:       9rtui"
