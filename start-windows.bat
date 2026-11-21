@echo off
rem Starts PrintHarbor. It runs in the system tray (the anchor icon by the clock),
rem not in a window: click the icon to open it, right-click for Restart and Quit.
rem It opens your browser by itself once it is ready (or straight away if it is
rem already running). You can also just double-click the program in dist.
cd /d "%~dp0"
rem An update delivered while PrintHarbor was running waits here; swap it in now.
if exist "dist\printharbor-windows-amd64.new.exe" (
  move /y "dist\printharbor-windows-amd64.new.exe" "dist\printharbor-windows-amd64.exe" >nul 2>nul
)
start "" "dist\printharbor-windows-amd64.exe" -config "%~dp0config.json" -open
