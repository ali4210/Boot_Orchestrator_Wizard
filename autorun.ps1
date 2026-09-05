<#
==============================================================================
 autorun.ps1 — Universal Boot Orchestrator Windows Runtime Launcher
 Auto-elevating bootstrapper with native UAC prompt trigger.
==============================================================================
#>
[CmdletBinding()]
param()

# --- 1. SELF-ELEVATION TRAMPOLINE (TRIGGERS NATIVE UAC "YES" PROMPT) ---
$currentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($currentIdentity)
$isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    # Resolve the exact path of this script
    $scriptPath = $MyInvocation.MyCommand.Definition
    if (-not $scriptPath) {
        $scriptPath = "$PSScriptRoot\autorun.ps1"
    }

    # Construct the argument list to re-launch PowerShell elevated in the current folder
    $arguments = "-NoProfile -ExecutionPolicy Bypass -NoExit -Command `"Set-Location '$PSScriptRoot'; & '$scriptPath'`""

    try {
        # Trigger native Windows UAC modal dialog via 'RunAs'
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

# Enable ANSI escape sequences in Windows Terminal/ConHost
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$Host.UI.RawUI.WindowTitle = "Universal Boot Orchestrator [ENTERPRISE ADMIN]"

Clear-Host
Write-Host "======================================================================" -ForegroundColor Cyan
Write-Host " UNIVERSAL OS BOOT ORCHESTRATOR :: ELEVATED WINDOWS RUNTIME" -ForegroundColor Cyan
Write-Host " Privilege Status: Administrator (Full Hardware Block Access Granted)" -ForegroundColor Green
Write-Host "======================================================================" -ForegroundColor Cyan

# --- 3. RUNTIME BINARY DISCOVERY OR COMPILATION ---
$BinPath = Join-Path $ScriptDir "bin\boot-orchestrator.exe"

# If the binary is already compiled and present, launch immediately
if (Test-Path $BinPath) {
    Write-Host "=> Native engine detected: $BinPath" -ForegroundColor Green
    Write-Host "=> Initializing hardware orchestrator..." -ForegroundColor Cyan
    & $BinPath
    exit $LASTEXITCODE
}

# Fallback: Compile binary if missing
Write-Host "=> Binary missing. Checking Go toolchain to build from source..." -ForegroundColor Yellow
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "=> Go compiler not found in system PATH." -ForegroundColor Yellow
    Write-Host "=> Auto-fetching portable Go runtime..." -ForegroundColor Cyan
    
    $GoZip = "$env:TEMP\go_bootstrap.zip"
    $GoExtract = "$ScriptDir\.runtime"
    $DownloadUrl = "https://go.dev/dl/go1.22.6.windows-amd64.zip"
    
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $GoZip -UseBasicParsing
    Expand-Archive -Path $GoZip -DestinationPath $GoExtract -Force
    Remove-Item $GoZip -Force
    $env:PATH = "$GoExtract\go\bin;" + $env:PATH
}

Write-Host "=> Compiling cmd/orchestrator for Windows amd64..." -ForegroundColor Cyan
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

if (-not (Test-Path "$ScriptDir\bin")) {
    New-Item -ItemType Directory -Path "$ScriptDir\bin" | Out-Null
}

go build -ldflags="-s -w" -o $BinPath .\cmd\orchestrator
if ($LASTEXITCODE -ne 0) {
    Write-Host "[-] Compilation failed." -ForegroundColor Red
    exit 1
}

Write-Host "=> Compilation successful. Launching orchestrator..." -ForegroundColor Green
& $BinPath
exit $LASTEXITCODE
