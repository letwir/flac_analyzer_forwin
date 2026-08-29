@echo off
chcp 65001 > nul
setlocal enabledelayedexpansion

echo ========================================================================
echo   Flac_Analyzer - 全自動ワンタップ初期化 ＆ ビルドスクリプトですわ！
echo ========================================================================
echo.

rem -------------------------------------------------------------------------
rem 1. Python 環境の検出
rem -------------------------------------------------------------------------
echo [Step 1/3] Python 環境を検出しておりますわ...
set PYTHON_CMD=python.exe
!PYTHON_CMD! --version >nul 2>&1
if errorlevel 1 (
    set PYTHON_CMD=py.exe
    !PYTHON_CMD! --version >nul 2>&1
    if errorlevel 1 (
        echo [ERROR] Python (python.exe / py.exe) が PATH に見つかりませんわ！
        echo Python 3.12 または 3.13 をインストールの上、環境変数 PATH に追加してくださいませ。
        exit /b 1
    )
)
echo  └─ 検出成功: !PYTHON_CMD!

rem -------------------------------------------------------------------------
rem 2. モデルダウンロード・.pb->.onnx変換・Python環境構築
rem -------------------------------------------------------------------------
echo.
echo [Step 2/3] モデル取得・ONNX自動変換・依存セットアップを実行いたしますわ...
!PYTHON_CMD! init_dl_model.py
if errorlevel 1 (
    echo [WARNING] init_dl_model.py の実行中に一部警告/エラーが発生いたしましたわ。
)

rem -------------------------------------------------------------------------
rem 3. Go オーケストレーターのコンパイル ＆ 配置
rem -------------------------------------------------------------------------
echo.
echo [Step 3/3] Go オーケストレーターのコンパイルを開始いたしますわ...
go version >nul 2>&1
if errorlevel 1 goto NO_GO

cd /d "%~dp0orchestrator"
go build -v -o orchestrator.exe .
if errorlevel 1 goto BUILD_FAIL
go build -v -o "%~dp0single-orchestrator.exe" .
if errorlevel 1 goto BUILD_FAIL

cd /d "%~dp0"
copy /Y "%~dp0orchestrator\orchestrator.exe" "%~dp0orchestrator.exe" >nul

echo.
echo ========================================================================
echo   ✨ 初期セットアップ ＆ オーケストレータービルドが大成功いたしましたわ！
echo ========================================================================
echo.
echo 実行ファイル: %~dp0orchestrator.exe
echo 単発実行用  : %~dp0single-orchestrator.exe
echo.
echo 🚀 以下の手順で解析を開始できますわ:
echo   1. ルート直下で Go オーケストレーターを起動:
echo      .\orchestrator.exe
echo.
echo   2. 別ウィンドウの PowerShell からディレクトリ走査バッチを実行:
echo      .\run_batch.ps1 -Dir "D:\Music\FLAC_Library"
echo.
goto END

:NO_GO
echo [ERROR] Go コンパイラ (go.exe) が PATH に見つかりませんわ！
echo Go 1.22 以上をインストールの上、環境変数 PATH に追加してくださいませ。
cd /d "%~dp0"
exit /b 1

:BUILD_FAIL
echo [ERROR] Go オーケストレーターのビルドに失敗いたしましたの！エラーログをご確認くださいませ。
cd /d "%~dp0"
exit /b 1

:END
endlocal
