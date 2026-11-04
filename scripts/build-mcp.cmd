@echo off
setlocal
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build-mcp.ps1" %*
exit /b %errorlevel%
