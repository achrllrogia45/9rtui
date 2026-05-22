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

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".accounts") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir ".tui-logs\full-backups") | Out-Null
New-Item -ItemType Directory -Force -Path $CacheDir | Out-Null

function Ensure-Go {
    if (Get-Command go -ErrorAction SilentlyContinue) { return }

    $LocalGo = Join-Path $InstallDir "cache\go"
    $LocalGoBin = Join-Path $LocalGo "bin"
    $LocalGoExe = Join-Path $LocalGoBin "go.exe"
    if (Test-Path $LocalGoExe) {
        $env:PATH = "$LocalGoBin;$env:PATH"
        return
    }

    $Arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
    switch ($Arch) {
        "x64" { $GoArch = "amd64" }
        "arm64" { $GoArch = "arm64" }
        default { Fail "unsupported architecture for local Go install: $Arch" }
    }

    Say "go not found; installing local Go into $LocalGo"
    $Feed = Invoke-RestMethod -Uri "https://go.dev/dl/?mode=json"
    $File = $Feed[0].files | Where-Object { $_.os -eq "windows" -and $_.arch -eq $GoArch -and $_.filename -like "*.zip" } | Select-Object -First 1
    if (!$File) { Fail "could not find Go windows/$GoArch zip" }

    $GoZip = Join-Path $CacheDir $File.filename
    if (!(Test-Path $GoZip) -or $env:NRTUI_REFRESH -eq "1") {
        Say "download: https://go.dev/dl/$($File.filename)"
        Invoke-WebRequest -Uri "https://go.dev/dl/$($File.filename)" -OutFile $GoZip
    } else {
        Say "using cached Go archive: $GoZip"
    }

    $GoExtract = Join-Path $CacheDir "go-extract"
    if (Test-Path $GoExtract) { Remove-Item -Recurse -Force $GoExtract }
    New-Item -ItemType Directory -Force -Path $GoExtract | Out-Null
    Expand-Archive -Force -Path $GoZip -DestinationPath $GoExtract
    if (Test-Path $LocalGo) { Remove-Item -Recurse -Force $LocalGo }
    New-Item -ItemType Directory -Force -Path (Split-Path $LocalGo) | Out-Null
    Move-Item -Force (Join-Path $GoExtract "go") $LocalGo
    $env:PATH = "$LocalGoBin;$env:PATH"
}

Ensure-Go

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
