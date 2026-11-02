@echo off
setlocal
cd /d "%~dp0"
call "%~dp0stop.bat"
call "%~dp0start.bat"
endlocal
