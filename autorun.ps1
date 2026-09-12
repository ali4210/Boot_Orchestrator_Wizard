<#
==============================================================================
 autorun.ps1 — Universal Boot Orchestrator Windows Runtime Launcher
 Auto-elevating bootstrapper with native UAC prompt trigger and console setup.
 Supports:
   .\autorun.ps1              -> Standard elevated launch
   .\autorun.ps1 -Force (-f)  -> Force clean rebuild + launch
   .\autorun.ps1 -Hard        -> Deep clean temporary artifacts + rebuild + launch
==============================================================================
#>
[CmdletBinding()]
param(
    [Alias("f")]
    [switch]$Force,

    [Alias("hard")]
    [switch]$Hard,

    [Alias("h")]
    [switch]$Help
)

if ($Help) {
    Write-Host "Universal Boot Orchestrator :: Windows PowerShell Help" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Usage:"
    Write-Host "  .\autorun.ps1         Standard elevated launch"
    Write-Host "  .\autorun.ps1 -f      Force clean rebuild + auto-resolve modules + launch"
    Write-Host "  .\autorun.ps1 -hard   Deep purge of caches, journals + fresh rebuild + launch"
    Write-Host "  .\autorun.ps1 -h      Display this help menu"
    Write-Host ""
    exit 0
}

# --- 1. SELF-ELEVATION TRAMPOLINE (TRIGGERS NATIVE UAC "YES" PROMPT) ---
$currentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($currentIdentity)
$isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    $scriptPath = $MyInvocation.MyCommand.Definition
    if (-not $scriptPath) {
        $scriptPath = Join-Path $PSScriptRoot "autorun.ps1"
    }

    $paramList = @()
    if ($Force) { $paramList += "-Force" }
    if ($Hard)  { $paramList += "-Hard" }
    $paramString = $paramList -join " "

    $arguments = "-NoProfile -ExecutionPolicy Bypass -Command `"Set-Location '$PSScriptRoot'; & '$scriptPath' $paramString`""

    try {
        Start-Process -FilePath "powershell.exe" -ArgumentList $arguments -Verb RunAs
        exit 0
    } catch {
        Write-Host "[-] Elevation declined or cancelled by user." -ForegroundColor Red
        exit 1
    }
}

# --- 2. ELEVATED ENVIRONMENT INITIALIZATION ---
$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $ScriptDir) {
    $ScriptDir = (Get-Location).Path
}
Set-Location $ScriptDir

# Enable ANSI escape sequences & UTF-8 in Windows Terminal / ConHost
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$Host.UI.RawUI.WindowTitle = "Universal Boot Orchestrator [ENTERPRISE ADMIN]"

Clear-Host
Write-Host "======================================================================" -ForegroundColor Cyan
Write-Host " UNIVERSAL OS BOOT ORCHESTRATOR :: ELEVATED WINDOWS RUNTIME" -ForegroundColor Cyan
Write-Host " Privilege Status: Administrator (Full Hardware Block Access Granted)" -ForegroundColor Green
Write-Host "======================================================================" -ForegroundColor Cyan

# --- 3. HOUSEKEEPING & PURGE ROUTINE (-Hard / -Force) ---
$BinPath = Join-Path $ScriptDir "bin\boot-orchestrator.exe"

if ($Hard -or $Force) {
    Write-Host "=> [HOUSEKEEPING] Cleaning build artifacts and temporary files..." -ForegroundColor Yellow
    
    $legacyRuntime = Join-Path $ScriptDir ".runtime"
    if (Test-Path $legacyRuntime) {
        Write-Host "=> Purging deprecated local .runtime directory..." -ForegroundColor Yellow
        Remove-Item -Recurse -Force $legacyRuntime -ErrorAction SilentlyContinue
    }

    $journalPath = Join-Path $ScriptDir "orchestrator_journal.json"
    if ($Hard -and (Test-Path $journalPath)) {
        Write-Host "=> Purging incomplete transaction journals..." -ForegroundColor Yellow
        Remove-Item -Force $journalPath -ErrorAction SilentlyContinue
    }
    
    Get-ChildItem -Path $ScriptDir -Include *.tmp, *.part, *.log -Recurse -File | Remove-Item -Force -ErrorAction SilentlyContinue

    if (Test-Path $BinPath) {
        Write-Host "=> Removing existing binary for clean rebuild..." -ForegroundColor Yellow
        Remove-Item -Force $BinPath -ErrorAction SilentlyContinue
    }
}

# --- 4. RUNTIME BINARY DISCOVERY OR COMPILATION ---
if ((Test-Path $BinPath) -and (-not $Force) -and (-not $Hard)) {
    Write-Host "=> Native engine detected: $BinPath" -ForegroundColor Green
    Write-Host "=> Initializing hardware orchestrator..." -ForegroundColor Cyan
    & $BinPath
    exit $LASTEXITCODE
}

Write-Host "=> Checking Go toolchain to build from source..." -ForegroundColor Yellow

$tempGoRoot = "$env:TEMP\go_runtime"
$bootstrappedGo = $false

try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Host "=> Go compiler not found in system PATH." -ForegroundColor Yellow
        Write-Host "=> Auto-fetching portable Go runtime to ephemeral storage..." -ForegroundColor Cyan
        
        $GoZip = "$env:TEMP\go_bootstrap.zip"
        $DownloadUrl = "https://go.dev/dl/go1.22.6.windows-amd64.zip"
        
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $GoZip -UseBasicParsing
        
        if (Test-Path $tempGoRoot) {
            Remove-Item -Recurse -Force $tempGoRoot -ErrorAction SilentlyContinue
        }
        
        Expand-Archive -Path $GoZip -DestinationPath $tempGoRoot -Force
        Remove-Item -Force $GoZip -ErrorAction SilentlyContinue
        
        $env:PATH = "$tempGoRoot\go\bin;" + $env:PATH
        $bootstrappedGo = $true
    }

    Write-Host "=> Compiling cmd/orchestrator for Windows amd64..." -ForegroundColor Cyan
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"

    $BinDir = Join-Path $ScriptDir "bin"
    if (-not (Test-Path $BinDir)) {
        New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    }

    go build -ldflags="-s -w" -o $BinPath .\cmd\orchestrator\main.go
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[-] Compilation failed." -ForegroundColor Red
        exit 1
    }

    Write-Host "=> Compilation successful." -ForegroundColor Green
}
finally {
    if ($bootstrappedGo -and (Test-Path $tempGoRoot)) {
        Write-Host "=> Purging temporary Go bootstrap cache from TEMP..." -ForegroundColor Gray
        Remove-Item -Recurse -Force $tempGoRoot -ErrorAction SilentlyContinue
    }
}

# --- 5. LAUNCH THE ORCHESTRATOR ---
Write-Host "=> Launching Universal Boot Orchestrator..." -ForegroundColor Green
& $BinPath
exit $LASTEXITCODE