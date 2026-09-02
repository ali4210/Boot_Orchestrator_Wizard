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

:: ------------------------------------------------------------------------------
:: 1. Anchor Working Directory & Early UAC Elevation Gate
:: ------------------------------------------------------------------------------
cd /d "%~dp0"

net session >nul 2>&1
if %errorlevel% neq 0 (
    echo ======================================================================
    echo  [SECURITY NOTICE] Partitioning and BCD operations require Administrator.
    echo  Requesting UAC elevation...
    echo ======================================================================
    powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process cmd.exe -ArgumentList '/c \"\"%~f0\" %*\"' -Verb RunAs"
    exit /b
)

set "MODE=NORMAL"
for %%a in (%*) do (
    if "%%a"=="-f" set "MODE=FORCE"
    if "%%a"=="--force" set "MODE=FORCE"
    if "%%a"=="-hard" set "MODE=HARD"
    if "%%a"=="--hard" set "MODE=HARD"
    if "%%a"=="-f-hard" set "MODE=HARD"
)

echo ======================================================================
echo  UNIVERSAL OS ^& DUAL-BOOT ORCHESTRATOR :: LAUNCHER [MODE: %MODE%]
echo ======================================================================

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
:: 3. Runtime Verification & Bootstrapper (Min Go 1.22.6)
:: ------------------------------------------------------------------------------
set "REQUIRED_MAJOR=1"
set "REQUIRED_MINOR=22"
set "GO_BIN="

if "%MODE%" neq "HARD" (
    where go >nul 2>&1
    if !errorlevel! equ 0 (
        for /f "tokens=3" %%v in ('go version') do (
            set "RAW_VER=%%v"
            set "SYS_GO_VER=!RAW_VER:go=!"
        )
        for /f "tokens=1,2 delims=." %%a in ("!SYS_GO_VER!") do (
            set "GO_MAJ=%%a"
            set "GO_MIN=%%b"
        )
        if !GO_MAJ! geq %REQUIRED_MAJOR% (
            if !GO_MIN! geq %REQUIRED_MINOR% (
                echo [+] Valid System Go runtime detected: v!SYS_GO_VER!
                set "GO_BIN=go"
            )
        )
        if "!GO_BIN!"=="" (
            echo [-] Installed Go runtime ^(v!SYS_GO_VER!^) is outdated ^(^< 1.22^).
        )
    )
)

if "%GO_BIN%"=="" (
    set "PORTABLE_DIR=%~dp0.runtime\go"
    set "PORTABLE_BIN=%~dp0.runtime\go\bin\go.exe"

    if exist "!PORTABLE_BIN!" (
        echo [+] Using isolated local portable Go runtime.
        set "GO_BIN=!PORTABLE_BIN!"
    ) else (
        echo [-] Bootstrapping isolated Go 1.22.6 toolchain...
        if not exist "%~dp0.runtime" mkdir "%~dp0.runtime"
        set "GO_ZIP=%~dp0.runtime\go_bootstrap.zip"
        set "GO_URL=https://go.dev/dl/go1.22.6.windows-amd64.zip"

        powershell -NoProfile -Command "[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; Write-Host '=> Downloading toolchain...'; Invoke-WebRequest -Uri '!GO_URL!' -OutFile '!GO_ZIP!'"
        if !errorlevel! neq 0 (
            echo [FATAL] Go download failed. Check network connectivity.
            pause
            exit /b 1
        )

        echo [*] Extracting runtime engine...
        powershell -NoProfile -Command "Expand-Archive -Path '!GO_ZIP!' -DestinationPath '%~dp0.runtime' -Force"
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
:: 4. Module Dependency Sync & Compilation
:: ------------------------------------------------------------------------------
echo [*] Synchronizing module dependencies...
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

:: ------------------------------------------------------------------------------
:: 5. Execution (With Full Argument Forwarding)
:: ------------------------------------------------------------------------------
echo [*] Launching Orchestrator...
bin\boot-orchestrator.exe %*

set "EXITCODE=%errorlevel%"
if %EXITCODE% neq 0 (
    echo.
    echo [!] Orchestrator exited with status %EXITCODE%.
    pause
    exit /b %EXITCODE%
)

endlocal