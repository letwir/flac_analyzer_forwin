@echo off
chcp 65001 > nul
echo ========================================================
echo   FLAC Analyzer - Orchestrator Build Script
echo ========================================================
echo.

echo --- Checking Go environment ---
go version >nul 2>&1
if errorlevel 1 goto NO_GO

echo --- Building Go Orchestrator ---
cd /d "%~dp0orchestrator"
go build -v -o orchestrator.exe .
if errorlevel 1 goto BUILD_FAIL

cd /d "%~dp0"
copy /Y "%~dp0orchestrator\orchestrator.exe" "%~dp0orchestrator.exe" >nul

echo.
echo === Orchestrator built successfully ===
echo Executable: %~dp0orchestrator\orchestrator.exe
echo Copied to:  %~dp0orchestrator.exe
echo.
echo You can now launch Orchestrator from root via:
echo   orchestrator.exe
echo.
goto END

:NO_GO
echo ERROR: Go compiler (go.exe) was not found in PATH!
echo Please install Go or add it to your System PATH environment variables.
exit /b 1

:BUILD_FAIL
echo ERROR: go build failed! Check errors above.
cd /d "%~dp0"
exit /b 1

:END
