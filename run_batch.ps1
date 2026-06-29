<#
.SYNOPSIS
    FLAC Analyzer 用の PowerShell 7 順次実行バッチスクリプトですわ！
    GENRE-SUB フォルダ単位で python main.py を都度プロセス起動し、RAM断片化を防ぎますの。

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
    [switch]$Skip,
    [switch]$Rough
)

$ErrorActionPreference = "Stop"
$env:PYTHONUTF8 = 1
# PowerShellの出力エンコーディングを完全にUTF-8へ切り替えますわ！
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

# テストモードのセットアップ
if ($Test) {
    Write-Host "[Test Mode] 一時ディレクトリにダミー構成を作成してテストを行いますわ！" -ForegroundColor Yellow
    $tempRoot = Join-Path $env:TEMP "flac_analyzer_test_root"
    if (Test-Path $tempRoot) {
        Remove-Item -Path $tempRoot -Recurse -Force | Out-Null
    }
    New-Item -ItemType Directory -Path $tempRoot | Out-Null

    # ダミーの GENRE-MAIN / GENRE-SUB 構成作成
    # フォルダ1: FLACあり
    $sub1 = New-Item -ItemType Directory -Path (Join-Path $tempRoot "J-POP\Artist-A")
    New-Item -ItemType File -Path (Join-Path $sub1.FullName "track1.flac") -Value "dummy flac content" | Out-Null
    # フォルダ2: FLACあり
    $sub2 = New-Item -ItemType Directory -Path (Join-Path $tempRoot "Rock\Artist-B")
    New-Item -ItemType File -Path (Join-Path $sub2.FullName "track2.flac") -Value "dummy flac content" | Out-Null
    # フォルダ3: FLACなし (スキップ対象)
    $sub3 = New-Item -ItemType Directory -Path (Join-Path $tempRoot "Anime\Artist-C")

    $MusicRoot = $tempRoot
    Write-Host "[Test Mode] テスト用ルートディレクトリ: $MusicRoot" -ForegroundColor Yellow

    # テスト用のダミー Python ターゲット作成
    $dummyPythonScript = Join-Path $PSScriptRoot "dummy_target.py"
    $dummyCode = @"
import sys
import os
filepath = sys.argv[1]
dir_abs = os.path.dirname(os.path.abspath(filepath))
genre_sub_name = os.path.basename(dir_abs)
genre_main_name = os.path.basename(os.path.dirname(dir_abs))
log_file_name = f"log_{genre_main_name}__{genre_sub_name}.log"
project_root = os.path.dirname(os.path.abspath(__file__))
log_file_path = os.path.join(project_root, log_file_name)

with open(log_file_path, "a", encoding="utf-8") as f:
    f.write(f"[Direct-Process] OK: {os.path.abspath(filepath)}\n")

print(f"[Dummy Target] Pythonが正常に起動されましたわ！ (Target: {filepath})")
sys.exit(0)
"@
    Set-Content -Path $dummyPythonScript -Value $dummyCode -Encoding utf8
    $targetScript = $dummyPythonScript
} else {
    $targetScript = Join-Path $PSScriptRoot "main.py"
    if (-not (Test-Path $targetScript)) {
        Write-Error "main.py がスクリプトと同じディレクトリに見つかりませんわ: $PSScriptRoot"
        exit 1
    }
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

# 単一の完了マークファイル flac.done の準備とロードしますわ
$doneFilePath = Join-Path $PSScriptRoot "flac.done"
$completedFiles = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)

if ($Skip) {
    if (-not (Test-Path $doneFilePath)) {
        Write-Host "💡 過去のログファイルから完了履歴を flac.done に移行していますわ..." -ForegroundColor Yellow
        $migratedPaths = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        $logFiles = Get-ChildItem -Path $PSScriptRoot -Filter "log_*.log" -File
        foreach ($logFile in $logFiles) {
            try {
                $lines = Get-Content -Path $logFile.FullName -Encoding utf8
                foreach ($line in $lines) {
                    if ($line -match "\[Direct-Process\] OK:\s*(.*)") {
                        $null = $migratedPaths.Add($Matches[1].Trim())
                    }
                }
            } catch {}
        }
        if ($migratedPaths.Count -gt 0) {
            $contentStr = [System.String]::Join([System.Environment]::NewLine, @($migratedPaths)) + [System.Environment]::NewLine
            [System.IO.File]::WriteAllText($doneFilePath, $contentStr, [System.Text.Encoding]::UTF8)
            Write-Host "🟢 $($migratedPaths.Count) 件の履歴を flac.done に移行いたしましたの！" -ForegroundColor Green
        } else {
            [System.IO.File]::WriteAllText($doneFilePath, "", [System.Text.Encoding]::UTF8)
        }
    }

    # flac.done のロード
    try {
        $doneLines = Get-Content -Path $doneFilePath -Encoding utf8
        foreach ($line in $doneLines) {
            if (-not [System.String]::IsNullOrWhiteSpace($line)) {
                $null = $completedFiles.Add($line.Trim())
            }
        }
        Write-Host "🟢 flac.done から $($completedFiles.Count) 件の完了パスをロードいたしましたわ！" -ForegroundColor Green
    } catch {
        Write-Host "⚠️ flac.done の読み込みに失敗しましたわ: $_" -ForegroundColor Yellow
    }
}

# 1層目: GENRE-MAIN を走査
$genreMains = Get-ChildItem -Path $MusicRoot -Directory
$processedCount = 0
$skippedCount = 0

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

        # 解析対象のフォルダ名から、プロジェクト直下に置くユニークなログファイル名を生成しますわ
        $genreMainName = Split-Path (Split-Path $genreSub.FullName -Parent) -Leaf
        $genreSubName = $genreSub.Name
        $logFileName = "log_${genreMainName}__${genreSubName}.log"
        $logFilePath = Join-Path $PSScriptRoot $logFileName

        $subProcessedCount = 0
        $subSkippedCount = 0

        foreach ($flac in $flacs) {
            $flacPath = $flac.FullName

            # スキップ判定
            if ($Skip -and $completedFiles.Contains($flacPath)) {
                $subSkippedCount++
                $skippedCount++
                continue
            }

            $processedCount++
            $subProcessedCount++
            Write-Host ""
            Write-Host "==================================================" -ForegroundColor Cyan
            Write-Host "[$processedCount] 処理開始: $flacPath" -ForegroundColor Cyan
            Write-Host "==================================================" -ForegroundColor Cyan

            if ($DryRun) {
                $dryArgs = "--resume"
                if ($Rough) { $dryArgs += " --rough" }
                Write-Host "[DryRun] 実行予定コマンド: python `"$targetScript`" `"$flacPath`" $dryArgs" -ForegroundColor Gray
                continue
            }

            # Goオーケストレーターのキューへ投下
            try {
                $fileSize = (Get-Item $flacPath).Length
                $body = @{
                    flacPath = $flacPath
                    fileSize = $fileSize
                    targetScript = $targetScript
                } | ConvertTo-Json -Compress
                
                Invoke-RestMethod -Uri "http://127.0.0.1:8080/task" -Method Post -Body $body -ContentType "application/json" | Out-Null
                Write-Host "🟢 キューに投下いたしましたわ: $flacPath" -ForegroundColor Green
            }
            catch {
                Write-Host "❌ 実行エラーが発生いたしましたわ: $_" -ForegroundColor Red
            }
        }

        if ($subSkippedCount -gt 0) {
            Write-Host "  [-] スキップ (処理完了済み): $subSkippedCount 件 (サブフォルダ: $($genreSub.FullName))" -ForegroundColor Yellow
        }
    }
}

Write-Host ""
Write-Host "=========================================" -ForegroundColor Green
Write-Host " バッチ処理が終了いたしましたわ！"
Write-Host " 処理完了: $processedCount 件"
Write-Host " スキップ: $skippedCount 件"
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
