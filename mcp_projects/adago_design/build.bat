@echo off
setlocal
set GOWORK=off
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o bin\adago-design.exe .
exit /b %errorlevel%
