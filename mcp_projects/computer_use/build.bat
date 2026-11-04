@echo off
setlocal
set GOWORK=off
set CGO_ENABLED=0
go build -trimpath -o bin\computer-use.exe .
exit /b %errorlevel%
