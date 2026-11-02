@echo off
setlocal
cd /d "%~dp0"

if not exist "bin\clodesen2api.exe" (
  echo binary is missing: bin\clodesen2api.exe
  exit /b 1
)

if not exist "run.log" type nul > "run.log"
start "clodesen2api" /b "bin\clodesen2api.exe" -c "config.toml" >> "run.log" 2>&1
echo clodesen2api started. Log: %CD%\run.log
endlocal
