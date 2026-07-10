<#
.SYNOPSIS
    FLAC Analyzer 用の PowerShell 7 順次実行バッチスクリプトですわ！
    すべての FLAC ファイルを Go オーケストレーターに POST します。スキップ判定は Go 側の SQLite DB で一元管理されます。

.PARAMETER MusicRoot
    音楽ライブラリのルートパス（デフォルト: M:\Music\album）

.PARAMETER Test
    有効にすると、一時ディレクトリにダミー構成を作成して動作確認テストを行いますわ。

.PARAMETER DryRun
    有効にすると、コマンドを実行せずに、実行予定のコマンドを表示するだけにとどめますわ。
#>

param (
    [string]$MusicRoot = "M:\Music\album",
    [switch]$Test,
    [switch]$DryRun,
    [switch]$Rough
)

$ErrorActionPreference = "Stop"
$env:PYTHONUTF8 = 1
# PowerShellの出力エンコーディングを完全にUTF-8へ切り替えますわ！
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$stopWatch = [System.Diagnostics.Stopwatch]::StartNew()

# テストモードのセットアップ
if ($Test) {
    Write-Host "[Test Mode] 一時ディレクトリにダミー構成を作成してテストを行いますわ！" -ForegroundColor Yellow
    $tempRoot = Join-Path $env:TEMP "flac_analyzer_test_root"
    if (Test-Path $tempRoot) {
        Remove-Item -Path $tempRoot -Recurse -Force | Out-Null
    }
    New-Item -ItemType Directory -Path $tempRoot | Out-Null

    # ダミーの GENRE-MAIN / GENRE-SUB 構成作成
    $sub1 = New-Item -ItemType Directory -Path (Join-Path $tempRoot "J-POP\Artist-A")
    New-Item -ItemType File -Path (Join-Path $sub1.FullName "track1.flac") -Value "dummy flac content" | Out-Null
    $sub2 = New-Item -ItemType Directory -Path (Join-Path $tempRoot "Rock\Artist-B")
    New-Item -ItemType File -Path (Join-Path $sub2.FullName "track2.flac") -Value "dummy flac content" | Out-Null
    $sub3 = New-Item -ItemType Directory -Path (Join-Path $tempRoot "Anime\Artist-C")

    $MusicRoot = $tempRoot
    Write-Host "[Test Mode] テスト用ルートディレクトリ: $MusicRoot" -ForegroundColor Yellow

    # テスト用のダミー Python ターゲット作成
    $dummyPythonScript = Join-Path $PSScriptRoot "dummy_target.py"
    $dummyCode = @"
import sys
import os
filepath = sys.argv[1]
print(f"[Dummy Target] Pythonが正常に起動されましたわ！ (Target: {filepath})")
sys.exit(0)
"@
    Set-Content -Path $dummyPythonScript -Value $dummyCode -Encoding utf8
    $targetScript = $dummyPythonScript
} else {
    $targetScript = Join-Path $PSScriptRoot "main.py"
}

# ディレクトリ存在チェック
if (-not (Test-Path $MusicRoot)) {
    Write-Error "音楽ルートディレクトリが見つかりませんわ: $MusicRoot"
    exit 1
}

Write-Host "=========================================" -ForegroundColor Green
Write-Host " FLAC Analyzer Batch Run Starting..."
Write-Host " ルート: $MusicRoot"
Write-Host " ターゲット: $targetScript"
Write-Host "=========================================" -ForegroundColor Green

# Orchestratorの起動チェックと副窓での自動起動
$orchestratorProcess = Get-Process -Name "orchestrator" -ErrorAction SilentlyContinue
if (-not $orchestratorProcess) {
    Write-Host "💡 Orchestrator が起動していないため、別ウィンドウで自動起動いたしますわ！" -ForegroundColor Yellow
    $orchestratorExe = Join-Path $PSScriptRoot "orchestrator\orchestrator.exe"
    if (Test-Path $orchestratorExe) {
        Start-Process -FilePath $orchestratorExe -WorkingDirectory (Join-Path $PSScriptRoot "orchestrator")
        Start-Sleep -Seconds 2 # 起動を少し待ちますわ
    } else {
        Write-Host "⚠️ orchestrator.exe が見つかりませんわ！" -ForegroundColor Red
    }
} else {
    Write-Host "🟢 Orchestrator は既に起動済みですわ！" -ForegroundColor Green
}

# 1層目: GENRE-MAIN を走査
$genreMains = Get-ChildItem -Path $MusicRoot -Directory
$processedCount = 0

foreach ($genreMain in $genreMains) {
    # 2層目: GENRE-SUB を走査
    $genreSubs = Get-ChildItem -Path $genreMain.FullName -Directory
    foreach ($genreSub in $genreSubs) {
        # .flac ファイルが配下に再帰的に存在するかチェック
        $flacs = Get-ChildItem -Path $genreSub.FullName -Filter "*.flac" -Recurse -File
        if ($flacs.Count -eq 0) {
            Write-Host "  [-] スキップ (FLACなし): $($genreSub.FullName)" -ForegroundColor DarkGray
            continue
        }

        foreach ($flac in $flacs) {
            $flacPath = $flac.FullName
            $processedCount++
            
            Write-Host "[$processedCount] POSTing to Orchestrator: $flacPath" -ForegroundColor Cyan

            if ($DryRun) {
                Write-Host "[DryRun] 実行予定コマンド: POST http://127.0.0.1:8080/task (Target: $flacPath)" -ForegroundColor Gray
                continue
            }

            # Goオーケストレーターのキューへ投下
            try {
                $fileSize = (Get-Item -LiteralPath $flacPath).Length
                $body = @{
                    flacPath = $flacPath
                    fileSize = $fileSize
                    targetScript = $targetScript
                } | ConvertTo-Json -Compress
                
                $response = Invoke-RestMethod -Uri "http://127.0.0.1:8080/task" -Method Post -Body $body -ContentType "application/json" -ErrorAction Stop
                
                # We can't strictly read the exact status code easily with simple Invoke-RestMethod if it's not an error.
                # But it won't throw on 200 or 202.
                if ($response -match "Skipped") {
                    Write-Host "  [-] スキップ (Go判定済み): $flacPath" -ForegroundColor Yellow
                } else {
                    Write-Host "  [+] キューに投下いたしましたわ: $flacPath" -ForegroundColor Green
                }
            }
            catch {
                Write-Host "❌ 実行エラーが発生いたしましたわ: $_" -ForegroundColor Red
            }
        }
    }
}

Write-Host ""
Write-Host "=========================================" -ForegroundColor Green
Write-Host " バッチ処理(タスク投下)が終了いたしましたわ！"
Write-Host " 合計投下数: $processedCount 件"
$stopWatch.Stop()
Write-Host " 投下所要時間: $($stopWatch.Elapsed.ToString())"
Write-Host "=========================================" -ForegroundColor Green

# テストモードのクリーンアップ
if ($Test) {
    if (Test-Path $tempRoot) {
        Remove-Item -Path $tempRoot -Recurse -Force | Out-Null
    }
    if (Test-Path $dummyPythonScript) {
        Remove-Item -Path $dummyPythonScript -Force | Out-Null
    }
}
