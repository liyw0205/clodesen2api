@echo off
setlocal
cd /d "%~dp0"

taskkill /IM clodesen2api.exe /F >nul 2>&1
if errorlevel 1 (
  echo clodesen2api is not running.
) else (
  echo clodesen2api stopped.
)
endlocal
