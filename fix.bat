@echo off
setlocal
"%~dp0.venv\Scripts\python.exe" -B "%~dp0fix_empty_json.py" %*
exit /b %errorlevel%
