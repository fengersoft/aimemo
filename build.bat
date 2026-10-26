@echo off
setlocal enabledelayedexpansion

REM aimemo build script
REM injects version/commit/date into binary

for /f "tokens=*" %%i in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%i
if "%COMMIT%"=="" set COMMIT=unknown

for /f "tokens=*" %%i in ('git describe --tags --always 2^>nul') do set VERSION=%%i
if "%VERSION%"=="" set VERSION=dev

for /f "tokens=*" %%i in ('powershell -NoProfile -Command "Get-Date -Format yyyy-MM-ddTHH:mm:ssZ" 2^>nul') do set DATE=%%i
if "%DATE%"=="" set DATE=%date% %time%

if not exist bin mkdir bin

echo Building aimemo...
echo   version: %VERSION%
echo   commit:  %COMMIT%
echo   date:    %DATE%

go build -ldflags "-s -w -X main.version=%VERSION% -X main.commit=%COMMIT% -X main.date=%DATE%" -o bin\aimemo.exe .

if %ERRORLEVEL% equ 0 (
    echo.
    echo Build successful: bin\aimemo.exe
    bin\aimemo.exe version
) else (
    echo.
    echo Build failed!
    exit /b %ERRORLEVEL%
)

endlocal
