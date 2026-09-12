@echo off
setlocal EnableDelayedExpansion
title Universal Boot Orchestrator Launcher
cd /d "%~dp0"

:: 1. Check for Administrative Privileges
net session >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo ======================================================================
    echo  [SECURITY NOTICE] Hardware block operations require Administrator rights.
    echo  Invoking elevated PowerShell session...
    echo ======================================================================
)

:: 2. Execute autorun.ps1 with execution policy bypass and forward all parameters
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0autorun.ps1" %*

set "EXIT_CODE=%ERRORLEVEL%"
if %EXIT_CODE% NEQ 0 (
    echo.
    echo [-] Process terminated with exit code %EXIT_CODE%.
    pause
)

exit /b %EXIT_CODE%