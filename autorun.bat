@echo off
title Universal Boot Orchestrator Launcher
set SCRIPT_DIR=%~dp0
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& '%SCRIPT_DIR%autorun.ps1'"
exit /b %ERRORLEVEL%
