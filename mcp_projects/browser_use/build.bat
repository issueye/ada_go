@echo off
setlocal
set GOWORK=off
set CGO_ENABLED=0
go build -trimpath -ldflags "-H windowsgui" -o bin\browser-use.exe .
exit /b %errorlevel%
