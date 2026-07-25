<#
.SYNOPSIS
    FLAC Analyzer 用の PowerShell 7 順次実行バッチスクリプトですわ！
    すべての FLAC ファイルを Go オーケストレーターに POST しますわ。スキップ判定は Go 側の SQLite DB で一元管理されますの。

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
    [switch]$Rough,
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$env:PYTHONUTF8 = 1
# PowerShellの出力エンコーディングを完全にUTF-8へ切り替えますわ！
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$stopWatch = [System.Diagnostics.Stopwatch]::StartNew()

# テストモードのセットアップ (Phase 1: Gray)
if ($Test) {
    Write-Host "[Phase 1: Init] テストモード: 一時ディレクトリにダミー構成を作成して動作確認を行いますわ！" -ForegroundColor DarkGray
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
    Write-Host "[Phase 1: Init] テスト用ルートディレクトリを作成いたしましたわ: $MusicRoot" -ForegroundColor DarkGray

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
    Write-Host "❌ 致命的エラー: 指定された音楽ルートディレクトリが存在いたしませんわ: $MusicRoot" -ForegroundColor Red
    exit 1
}

Write-Host "=========================================" -ForegroundColor DarkGray
Write-Host " 🌹 FLAC Analyzer バッチ処理を開始いたしますわ！" -ForegroundColor Magenta
Write-Host " 📂 ルートパス  : $MusicRoot" -ForegroundColor Gray
Write-Host " 🎯 ターゲット  : $targetScript" -ForegroundColor Gray
Write-Host "=========================================" -ForegroundColor DarkGray

# Orchestratorの起動チェックと自動起動 (Phase 1: Init Gray/Yellow Warning)
$orchestratorProcess = Get-Process -Name "orchestrator" -ErrorAction SilentlyContinue
if (-not $orchestratorProcess) {
    Write-Host "⚠️ Orchestrator が起動していらっしゃらないため、自動起動いたしますわ！" -ForegroundColor DarkYellow
    $orchestratorExe = Join-Path $PSScriptRoot "orchestrator.exe"
    if (-not (Test-Path $orchestratorExe)) {
        $orchestratorExe = Join-Path $PSScriptRoot "orchestrator\orchestrator.exe"
    }

    if (Test-Path $orchestratorExe) {
        Start-Process -FilePath $orchestratorExe -WorkingDirectory $PSScriptRoot
        Start-Sleep -Seconds 2 # 起動を少し待ちますわ
    } else {
        Write-Host "❌ 致命的エラー: orchestrator.exe が見つかりませんわ！init.bat を実行してビルドなさってくださいませ。" -ForegroundColor Red
    }
} else {
    Write-Host "🔵 Phase 1: Orchestrator は既に起動済みでございますわ！" -ForegroundColor Blue
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
            Write-Host "  [-] スキップ (FLACファイル不在): $($genreSub.FullName)" -ForegroundColor DarkGray
            continue
        }

        foreach ($flac in $flacs) {
            $flacPath = $flac.FullName
            $processedCount++
            
            # Phase 2: Blue
            Write-Host "[$processedCount] 📤 Orchestrator へタスクを投下いたしますわ: $flacPath" -ForegroundColor DarkCyan

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
                    force = $Force.IsPresent
                } | ConvertTo-Json -Compress
                
                $response = Invoke-RestMethod -Uri "http://127.0.0.1:8080/task" -Method Post -Body $body -ContentType "application/json" -ErrorAction Stop
                
                if ($response -match "Skipped") {
                    # スキップ判定 (Phase 2: Cyan)
                    Write-Host "  [-] スキップ (GoオーケストレーターDB判定済みですの): $flacPath" -ForegroundColor Cyan
                } else {
                    # 投下完了 (Phase 5/6: Magenta/Purple)
                    Write-Host "  [+] キューへの投下が無事に完了いたしましたわ: $flacPath" -ForegroundColor Magenta
                }
            }
            catch {
                Write-Host "❌ 実行エラーが発生いたしましたわ: $_" -ForegroundColor Red
            }
        }
    }
}

Write-Host ""
Write-Host "=========================================" -ForegroundColor Magenta
Write-Host " 🎉 バッチ処理(タスク投下)が無事に終了いたしましたわ！" -ForegroundColor DarkMagenta
Write-Host " 📊 合計投下数  : $processedCount 件" -ForegroundColor White
$stopWatch.Stop()
Write-Host " ⏱️ 投下所要時間: $($stopWatch.Elapsed.ToString())" -ForegroundColor White
Write-Host "=========================================" -ForegroundColor Magenta

# テストモードのクリーンアップ
if ($Test) {
    if (Test-Path $tempRoot) {
        Remove-Item -Path $tempRoot -Recurse -Force | Out-Null
    }
    if (Test-Path $dummyPythonScript) {
        Remove-Item -Path $dummyPythonScript -Force | Out-Null
    }
}
