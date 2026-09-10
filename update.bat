@echo off
setlocal
pwsh.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0update.ps1" %*
exit /b %errorlevel%
