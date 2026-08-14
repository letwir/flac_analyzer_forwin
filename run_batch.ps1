<#
.SYNOPSIS
    FLAC Analyzer 用の PowerShell 7 並列タスク投下バッチスクリプトですわ！
    すべての FLAC ファイルを Go オーケストレーターに並列 POST しますわ。スキップ判定は Go 側の SQLite DB で一元管理されますの。

.PARAMETER MusicRoot
    音楽ライブラリのルートディレクトリ、または単一の FLAC ファイルパス（エイリアス: -Path, -File / デフォルト: M:\Music\album）

.PARAMETER Concurrency
    Go オーケストレーターへのタスク並列投下スレッド数（デフォルト: 8）

.PARAMETER Test
    有効にすると、一時ディレクトリにダミー構成を作成して動作確認テストを行いますわ。

.PARAMETER DryRun
    有効にすると、コマンドを実行せずに、実行予定のコマンドを表示するだけにとどめますわ。
#>

param (
    [Alias("Path", "File")]
    [string]$MusicRoot = "M:\Music\album",
    [int]$Concurrency = 8,
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

# スレッドセーフな進捗カウンター用クラス (C# ネイティブ型で全 Runspace 間共有)
if (-not ([System.Management.Automation.PSTypeName]'BatchCounter').Type) {
    Add-Type @"
    public static class BatchCounter {
        private static int count = 0;
        public static int Next() {
            return System.Threading.Interlocked.Increment(ref count);
        }
        public static void Reset() {
            count = 0;
        }
        public static int GetTotal() {
            return count;
        }
    }
"@
}
[BatchCounter]::Reset()

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

# パス存在チェック
if (-not (Test-Path $MusicRoot)) {
    Write-Host "❌ 致命的エラー: 指定されたパス（ファイルまたはディレクトリ）が存在いたしませんわ: $MusicRoot" -ForegroundColor Red
    exit 1
}

$isSingleFile = -not (Test-Path -Path $MusicRoot -PathType Container)

Write-Host "=========================================" -ForegroundColor DarkGray
Write-Host " 🌹 FLAC Analyzer バッチ処理を開始いたしますわ！" -ForegroundColor Magenta
if ($isSingleFile) {
    Write-Host " 📄 単一ファイル: $MusicRoot" -ForegroundColor Gray
} else {
    Write-Host " 📂 ルートパス  : $MusicRoot" -ForegroundColor Gray
}
Write-Host " 🎯 ターゲット  : $targetScript" -ForegroundColor Gray
Write-Host " ⚡ 並列投下数  : $Concurrency スレッド" -ForegroundColor Gray
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

# Phase 2: ファイル走査モードの判定 (単一ファイル / fd / rg / 標準走査)
$flacPaths = @()
$resolvedRoot = (Resolve-Path $MusicRoot).Path

if ($isSingleFile) {
    Write-Host "🎯 [Phase 2] 単一ファイル直接指定モードですわ！" -ForegroundColor Cyan
    $flacPaths = @($resolvedRoot)
} else {
    $fdCmd = Get-Command fd.exe -ErrorAction SilentlyContinue
    $rgCmd = Get-Command rg.exe -ErrorAction SilentlyContinue

    if ($fdCmd) {
        Write-Host "🦀⚡ [Phase 2] Rust高速モード(fd)起動ですわ！" -ForegroundColor Cyan
        $flacPaths = @(fd.exe -I -e flac -t f -a --search-path $resolvedRoot)
    } elseif ($rgCmd) {
        Write-Host "🦀⚡ [Phase 2] Rust高速モード(rg)起動ですわ！" -ForegroundColor Cyan
        $flacPaths = @(rg.exe --no-ignore --files -g "*.flac" $resolvedRoot)
    } else {
        Write-Host "🐢 [Phase 2] PowerShell標準フォールバックモードで走査いたしますわ..." -ForegroundColor DarkYellow
        $flacPaths = @([System.IO.Directory]::EnumerateFiles($resolvedRoot, "*.flac", [System.IO.SearchOption]::AllDirectories))
    }
}

$effectiveConcurrency = if ($isSingleFile -or $Concurrency -le 1) { 1 } else { $Concurrency }
$forceBool = $Force.IsPresent
$dryRunBool = $DryRun.IsPresent

# Phase 3: 並列キュー投下 (ForEach-Object -Parallel)
$flacPaths | ForEach-Object -ThrottleLimit $effectiveConcurrency -Parallel {
    $flacPath = $_
    if ([string]::IsNullOrWhiteSpace($flacPath)) { return }
    
    $idx = [BatchCounter]::Next()
    
    if ($using:dryRunBool) {
        Write-Host "[$idx] [DryRun] 実行予定コマンド: POST http://127.0.0.1:8080/task (Target: $flacPath)" -ForegroundColor Gray
        return
    }

    # Goオーケストレーターのキューへ投下
    try {
        $fileSize = [System.IO.FileInfo]::new($flacPath).Length
        $body = @{
            flacPath     = $flacPath
            fileSize     = $fileSize
            targetScript = $using:targetScript
            force        = $using:forceBool
        } | ConvertTo-Json -Compress
        
        $response = Invoke-RestMethod -Uri "http://127.0.0.1:8080/task" -Method Post -Body $body -ContentType "application/json" -ErrorAction Stop
        
        if ($response -like "Skipped*") {
            # スキップ判定 (Phase 2: Cyan)
            Write-Host "[$idx] [-] スキップ (GoオーケストレーターDB判定済みですの): $flacPath" -ForegroundColor Cyan
        } else {
            # 投下完了 (Phase 5/6: Magenta/Purple)
            Write-Host "[$idx] [+] キューへの投下が無事に完了いたしましたわ: $flacPath" -ForegroundColor Magenta
        }
    }
    catch {
        Write-Host "[$idx] ❌ 実行エラーが発生いたしましたわ: $flacPath ($_)" -ForegroundColor Red
    }
}

Write-Host ""
Write-Host "=========================================" -ForegroundColor Magenta
Write-Host " 🎉 バッチ処理(タスク投下)が無事に終了いたしましたわ！" -ForegroundColor DarkMagenta
Write-Host " 📊 合計投下数  : $([BatchCounter]::GetTotal()) 件" -ForegroundColor White
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
