@echo off
setlocal EnableDelayedExpansion
title Universal Boot Orchestrator Launcher
cd /d "%~dp0"

:: Forward directly through PowerShell launcher with argument preservation
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0autorun.ps1" %*

set "EXIT_CODE=%ERRORLEVEL%"
if %EXIT_CODE% NEQ 0 (
    echo.
    echo [-] Process terminated with exit code %EXIT_CODE%.
    pause
)

exit /b %EXIT_CODE%