@echo off
REM ============================================================
REM  IOT-TSDB one-click launcher (Windows)
REM  Double-click to start; press Ctrl+C in this window to stop.
REM  Edit the CONFIG section below to change defaults.
REM ============================================================
setlocal enableextensions
cd /d "%~dp0"

REM ---------- CONFIG (override via environment or edit here) ----------
if not defined LISTEN    set "LISTEN=:8080"
if not defined DATA_DIR  set "DATA_DIR=.\data"
if not defined RETENTION set "RETENTION=360h"
if not defined BLOCK     set "BLOCK=2h"
if not defined TITLE     set "TITLE=IOT-TSDB"

set "PORT=%LISTEN:*:=%"
set "URL=http://localhost:%PORT%/graph"

REM ---------- 1. Build binary if missing or /build requested ----------
if /i "%~1"=="/build" (
    echo [build] forced rebuild of iot-tsdb.exe ...
    go build -o iot-tsdb.exe ./cmd/iot-tsdb || goto :error
    call "%~f0" %2 %3 %4 %5 %6 %7 %8 %9
    exit /b %errorlevel%
)
if not exist "iot-tsdb.exe" (
    echo [build] iot-tsdb.exe not found, building now ...
    where go >nul 2>nul || (echo [error] Go toolchain not found in PATH & goto :error)
    go build -o iot-tsdb.exe ./cmd/iot-tsdb || goto :error
)

REM ---------- 2. Sanity checks ----------
if not exist "web\ui\static\react\index.html" (
    echo [warn]  Native UI assets not found at web\ui\static\react
    echo         Build them from web\ui: npm install ^&^& npm run build -w @prometheus-io/app
    echo         then move react-app\build to static\react. Server will still start.
)
netstat -ano | findstr /r /c:":%PORT% .*LISTENING" >nul && (
    echo [error] Port %PORT% is already in use. Set LISTEN=:xxxx and re-run.
    goto :error
)

REM ---------- 3. Open browser shortly after startup ----------
start "" /b powershell -NoProfile -WindowStyle Hidden -Command "Start-Sleep -Seconds 2; Start-Process '%URL%'"

REM ---------- 4. Run server in foreground (Ctrl+C to stop) ----------
echo [run]   IOT-TSDB starting on %LISTEN%  (UI: %URL%)
echo [run]   Data dir: %DATA_DIR%   Press Ctrl+C to stop.
iot-tsdb.exe -listen %LISTEN% -data-dir "%DATA_DIR%" -retention %RETENTION% -block-range %BLOCK% -page-title "%TITLE%" %*
goto :eof

:error
echo.
echo [failed] Startup aborted.
pause
exit /b 1
