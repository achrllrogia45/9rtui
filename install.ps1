$ErrorActionPreference = "Stop"

$Repo = if ($env:NRTUI_REPO) { $env:NRTUI_REPO } else { "achrllrogia45/9rtui" }
$Version = if ($env:NRTUI_VERSION) { $env:NRTUI_VERSION } else { $null }
$Branch = if ($env:NRTUI_BRANCH) { $env:NRTUI_BRANCH } else { $null }
$HomeDir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $HOME }
$InstallDir = if ($env:NRTUI_INSTALL_DIR) { $env:NRTUI_INSTALL_DIR } else { Join-Path $HomeDir ".9rtui" }
$ApiBase = if ($env:NRTUI_API) { $env:NRTUI_API } else { "http://localhost:20128" }
$DbPath = if ($env:NRTUI_DB) { $env:NRTUI_DB } else { Join-Path $env:APPDATA "9router\db\data.sqlite" }
$CacheDir = if ($env:NRTUI_CACHE_DIR) { $env:NRTUI_CACHE_DIR } else { Join-Path $InstallDir "tmp" }
$SrcDir = Join-Path $CacheDir "src"

function Say($Message) { Write-Host $Message }
function Fail($Message) { throw $Message }
function Resolve-Version {
    if ($script:Version) { return }
    $Api = "https://api.github.com/repos/$Repo/releases/latest"
    Say "resolve latest release: $Api"
    try {
        $Release = Invoke-RestMethod -Uri $Api -Headers @{ "User-Agent" = "9rtui-installer" }
        $script:Version = $Release.tag_name
    } catch {
        Fail "failed to resolve latest release from $Api; set NRTUI_VERSION. $($_.Exception.Message)"
    }
    if (!$script:Version) { Fail "latest release response missing tag_name; set NRTUI_VERSION" }
}
function Download-File($Url, $OutPath) {
    $Tmp = "$OutPath.tmp"
    if (Test-Path $Tmp) { Remove-Item -Force $Tmp }
    Invoke-WebRequest -Uri $Url -OutFile $Tmp
    Move-Item -Force $Tmp $OutPath
}
function Install-Exe($Cached, $Exe) {
    $StagedExe = Join-Path $InstallDir "9rtui.exe.new"
    $BackupExe = Join-Path $InstallDir "9rtui.exe.old"
    Copy-Item -Force $Cached $StagedExe
    try {
        if (Test-Path $BackupExe) { Remove-Item -Force $BackupExe }
        if (Test-Path $Exe) { Move-Item -Force $Exe $BackupExe }
        Move-Item -Force $StagedExe $Exe
        if (Test-Path $BackupExe) { Remove-Item -Force $BackupExe }
    } catch {
        if (Test-Path $StagedExe) { Remove-Item -Force $StagedExe -ErrorAction SilentlyContinue }
        if (!(Test-Path $Exe) -and (Test-Path $BackupExe)) { Move-Item -Force $BackupExe $Exe -ErrorAction SilentlyContinue }
        Fail "failed to replace $Exe. Close running 9rtui.exe, then rerun installer. mybad Windows locks running exe. Original: $($_.Exception.Message)"
    }
}

Resolve-Version
$SourceRef = if ($Branch) { $Branch } else { $Version }
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
    $Zip = Join-Path $CacheDir "9rtui-$SourceRef.zip"
    if ($Branch) { $Url = "https://github.com/$Repo/archive/refs/heads/$Branch.zip" } else { $Url = "https://github.com/$Repo/archive/refs/tags/$SourceRef.zip" }
    if (!(Test-Path $Zip) -or $env:NRTUI_REFRESH -eq "1") {
        Say "download scripts/source: $Url"
        Download-File $Url $Zip
    }
    $ExtractDir = Join-Path $CacheDir "extract"
    if (Test-Path $SrcDir) { Remove-Item -Recurse -Force $SrcDir }
    if (Test-Path $ExtractDir) { Remove-Item -Recurse -Force $ExtractDir }
    New-Item -ItemType Directory -Force -Path $ExtractDir | Out-Null
    Expand-Archive -Force -Path $Zip -DestinationPath $ExtractDir
    $Expanded = Get-ChildItem $ExtractDir -Directory | Select-Object -First 1
    if (!$Expanded) { Fail "failed to locate extracted source" }
    Move-Item -Force $Expanded.FullName $SrcDir
    $ScriptSource = Join-Path $SrcDir "scripts"
    if (!(Test-Path $ScriptSource)) { Fail "release source archive missing scripts directory: $ScriptSource" }
    $Scripts = Join-Path $InstallDir "scripts"
    if (Test-Path $Scripts) { Remove-Item -Recurse -Force $Scripts }
    Copy-Item -Recurse $ScriptSource $Scripts
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
    $Cached = Join-Path $CacheDir "$Asset-$Version"
    if (!(Test-Path $Cached) -or $env:NRTUI_REFRESH -eq "1") {
        Say "download binary: $Url"
        Download-File $Url $Cached
    }
    Install-Exe $Cached $Exe
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

Say "version:   $Version"
Say "installed: $Exe"
Say "scripts:   $(Join-Path $InstallDir "scripts")"
Say "config:    $IniPath"
Say "run:       9rtui"
