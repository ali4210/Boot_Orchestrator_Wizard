@echo off
REM ==============================================================================
REM autorun.bat -- Windows Autonomous Multi-Tier Launcher & Self-Healing Runtime
REM Supported Flags:
REM   (none)  : Standard build & execute
REM   -f      : Force rebuild (cleans build cache & bin/)
REM   -hard   : Complete deep purge (.runtime, module cache, state journals)
REM ==============================================================================
setlocal EnableDelayedExpansion
title Universal OS & Dual-Boot Orchestrator

set "MODE=NORMAL"
if "%~1"=="-f" set "MODE=FORCE"
if "%~1"=="--force" set "MODE=FORCE"
if "%~1"=="-hard" set "MODE=HARD"
if "%~1"=="--hard" set "MODE=HARD"
if "%~1"=="-f-hard" set "MODE=HARD"

echo ======================================================================
echo  UNIVERSAL OS ^& DUAL-BOOT ORCHESTRATOR :: LAUNCHER [MODE: %MODE%]
echo ======================================================================

:: ------------------------------------------------------------------------------
:: 1. Self-Elevation to Administrator
:: ------------------------------------------------------------------------------
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [*] Administrative privileges required. Elevating session...
    powershell -Command "Start-Process '%~f0' -ArgumentList '%*' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"

:: ------------------------------------------------------------------------------
:: 2. Self-Healing & Purge Logic
:: ------------------------------------------------------------------------------
if "%MODE%"=="HARD" (
    echo [*] [HARD PURGE] Purging binaries, cached runtime, and journals...
    if exist bin rmdir /s /q bin
    if exist .runtime rmdir /s /q .runtime
    if exist orchestrator_journal.json del /f /q orchestrator_journal.json
    where go >nul 2>&1
    if !errorlevel! equ 0 (
        go clean -cache -modcache -testcache >nul 2>&1
    )
    echo [*] [HARD PURGE] Complete system cleanup finished.
)

if "%MODE%"=="FORCE" (
    echo [*] [FORCE CLEAN] Purging binary artifacts and build cache...
    if exist bin rmdir /s /q bin
    where go >nul 2>&1
    if !errorlevel! equ 0 (
        go clean -cache >nul 2>&1
    )
)

:: ------------------------------------------------------------------------------
:: 3. Runtime Verification & Bootstrapper
:: ------------------------------------------------------------------------------
set "GO_BIN="
if "%MODE%" neq "HARD" (
    where go >nul 2>&1
    if !errorlevel! equ 0 (
        for /f "tokens=3" %%v in ('go version') do set "GOVERSTR=%%v"
        echo [+] System Go runtime detected: !GOVERSTR!
        set "GO_BIN=go"
    )
)

if "%GO_BIN%"=="" (
    set "PORTABLE_DIR=%~dp0.runtime\go"
    set "PORTABLE_BIN=%~dp0.runtime\go\bin\go.exe"

    if exist "!PORTABLE_BIN!" (
        echo [+] Using existing local portable Go runtime.
        set "GO_BIN=!PORTABLE_BIN!"
    ) else (
        echo [-] Go toolchain not available. Bootstrapping isolated Go 1.22.6...
        if not exist "%~dp0.runtime" mkdir "%~dp0.runtime"
        set "GO_ZIP=%~dp0.runtime\go_bootstrap.zip"
        set "GO_URL=https://go.dev/dl/go1.22.6.windows-amd64.zip"

        powershell -Command "[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; Write-Host '=> Downloading isolated Go runtime...'; Invoke-WebRequest -Uri '!GO_URL!' -OutFile '!GO_ZIP!'"
        if !errorlevel! neq 0 (
            echo [FATAL] Download failed. Check network connectivity.
            pause
            exit /b 1
        )

        echo [*] Extracting runtime engine...
        powershell -Command "Expand-Archive -Path '!GO_ZIP!' -DestinationPath '%~dp0.runtime' -Force"
        del /f /q "!GO_ZIP!" >nul 2>&1

        if not exist "!PORTABLE_BIN!" (
            echo [FATAL] Toolchain extraction failed.
            pause
            exit /b 1
        )
        set "GO_BIN=!PORTABLE_BIN!"
    )
)

:: ------------------------------------------------------------------------------
:: 4. Compilation & Execution
:: ------------------------------------------------------------------------------
echo [*] Resolving module dependencies...
"!GO_BIN!" mod tidy
"!GO_BIN!" mod download

echo [*] Compiling cmd\orchestrator...
if not exist bin mkdir bin
"!GO_BIN!" build -o bin\boot-orchestrator.exe .\cmd\orchestrator
if %errorlevel% neq 0 (
    echo [FATAL] Compilation failed.
    pause
    exit /b 1
)

echo [*] Launching Orchestrator...
bin\boot-orchestrator.exe

if %errorlevel% neq 0 (
    echo.
    echo [!] Orchestrator exited with code %errorlevel%.
    pause
    exit /b %errorlevel%
)

endlocal