@echo off
setlocal EnableDelayedExpansion
title Universal Boot Orchestrator Launcher
cd /d "%~dp0"

:: Forward directly through PowerShell launcher with argument preservation.
:: autorun.ps1 itself unconditionally self-elevates via UAC (Start-Process
:: -Verb RunAs) before doing anything privileged, so there is nothing for
:: this script to check or act on beforehand (audit item 2.16 — the
:: former "net session" admin pre-check here only ever logged a notice and
:: never gated anything on its result).
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0autorun.ps1" %*

set "EXIT_CODE=%ERRORLEVEL%"
if %EXIT_CODE% NEQ 0 (
    echo.
    echo [-] Process terminated with exit code %EXIT_CODE%.
    pause
)

exit /b %EXIT_CODE%