<#
.SYNOPSIS
    FLAC Analyzer 用の PowerShell 7 並列タスク投下バッチスクリプトですわ！
    すべての FLAC ファイルを Go オーケストレーターに並列 POST しますわ。スキップ判定は Go 側の SQLite DB で一元管理されますの。

.PARAMETER MusicRoot
    音楽ライブラリのルートディレクトリ、または単一の FLAC ファイルパス（エイリアス: -Dir, -Directory, -Path, -File, -TargetDir / デフォルト: M:\Music\album）

.PARAMETER Concurrency
    Go オーケストレーターへのタスク並列投下スレッド数（デフォルト: 8）

.PARAMETER Test
    有効にすると、一時ディレクトリにダミー構成を作成して動作確認テストを行いますわ。

.PARAMETER DryRun
    有効にすると、コマンドを実行せずに、実行予定のコマンドを表示するだけにとどめますわ。

.PARAMETER SingleTask
    常駐ETLを使用せず、FLACごとにGoを起動してCUEトラックを1件ずつ同期解析し、保存完了後に終了します。

.PARAMETER Unreg
    SingleTaskでSQLiteとPostgreSQLをトラック単位に照合し、双方で未登録または再試行可能なトラックだけを解析します。

.PARAMETER NoSync
    起動前のPostgreSQL解析済み曲リストのSQLite反映をスキップします。

.PARAMETER UnanalyzedOnly
    PostgreSQL (raw.library_flac) と照合し、DB未登録または features/predictions が空のファイルだけを抽出して優先スキャンします。

.PARAMETER PriorityOrder
    -UnanalyzedOnly 使用時の抽出優先順 (missing-first / empty-first / size-asc)。デフォルト: missing-first。

.PARAMETER InputList
    事前抽出した FLAC パス一覧ファイル（UTF-8 1行1パス）を直接スキャン対象として読み込みます。

.PARAMETER OutputList
    -UnanalyzedOnly で抽出した FLAC パス一覧を UTF-8 ファイルへ保存します。
#>

[CmdletBinding()]
param (
    [Parameter(Position = 0, ValueFromPipeline = $true, ValueFromPipelineByPropertyName = $true)]
    [Alias("Path", "File", "Dir", "Directory", "MusicDir", "TargetDir", "Target", "FilePath", "DirPath")]
    [string[]]$MusicRoot = @("M:\Music\album"),

    [Parameter(Position = 1)]
    [Alias("c", "Threads", "Parallel", "Jobs")]
    [int]$Concurrency = 8,

    [switch]$Test,
    [switch]$DryRun,
    [switch]$Rough,
    [switch]$Force,
    [switch]$SingleTask,
    [switch]$Unreg,
    [switch]$NoSync,
    [Alias("PgUnanalyzed", "PriorityScan")]
    [switch]$UnanalyzedOnly,
    [ValidateSet("missing-first", "empty-first", "size-asc")]
    [string]$PriorityOrder = "missing-first",
    [string]$InputList = "",
    [string]$OutputList = ""
)

$ErrorActionPreference = "Stop"

if ($Unreg -and -not $SingleTask) {
    Write-Host "❌ -Unreg は -SingleTask と組み合わせてください。" -ForegroundColor Red
    exit 2
}
if ($Unreg -and $Force) {
    Write-Host "❌ -Unreg と -Force は併用できません。" -ForegroundColor Red
    exit 2
}
if ($UnanalyzedOnly -and $Test) {
    Write-Host "❌ -UnanalyzedOnly と -Test は併用できません。" -ForegroundColor Red
    exit 2
}
if ($UnanalyzedOnly -and -not [string]::IsNullOrWhiteSpace($InputList)) {
    Write-Host "❌ -UnanalyzedOnly と -InputList は併用できません。" -ForegroundColor Red
    exit 2
}
if ($UnanalyzedOnly -and -not $PSBoundParameters.ContainsKey('MusicRoot')) {
    $MusicRoot = @("M:\Music\album", "N:\Music")
}

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

    $MusicRoot = @($tempRoot)
    Write-Host "[Phase 1: Init] テスト用ルートディレクトリを作成いたしましたわ: $tempRoot" -ForegroundColor DarkGray

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
if (-not [string]::IsNullOrWhiteSpace($InputList)) {
    if (-not (Test-Path -LiteralPath $InputList -PathType Leaf)) {
        Write-Host "❌ 致命的エラー: 指定された入力リストファイルが存在いたしませんわ: $InputList" -ForegroundColor Red
        exit 1
    }
    $isSingleFile = $false
} else {
    foreach ($rootItem in $MusicRoot) {
        if (-not (Test-Path -LiteralPath $rootItem) -and -not (Test-Path -Path $rootItem)) {
            Write-Host "❌ 致命的エラー: 指定されたパス（ファイルまたはディレクトリ）が存在いたしませんわ: $rootItem" -ForegroundColor Red
            exit 1
        }
    }
    $isSingleFile = if ($MusicRoot.Count -eq 1) {
        if (Test-Path -LiteralPath $MusicRoot[0]) {
            -not (Test-Path -LiteralPath $MusicRoot[0] -PathType Container)
        } else {
            -not (Test-Path -Path $MusicRoot[0] -PathType Container)
        }
    } else {
        $false
    }
}

Write-Host "=========================================" -ForegroundColor DarkGray
Write-Host " 🌹 FLAC Analyzer バッチ処理を開始いたしますわ！" -ForegroundColor Magenta
if (-not [string]::IsNullOrWhiteSpace($InputList)) {
    Write-Host " 📋 入力リスト  : $InputList" -ForegroundColor Gray
} elseif ($isSingleFile) {
    Write-Host " 📄 単一ファイル: $($MusicRoot[0])" -ForegroundColor Gray
} else {
    Write-Host " 📂 ルートパス  : $($MusicRoot -join ', ')" -ForegroundColor Gray
}
if ($UnanalyzedOnly) {
    Write-Host " 🎯 優先抽出    : PostgreSQL未登録/空データのみ ($PriorityOrder)" -ForegroundColor Cyan
}
Write-Host " 🎯 ターゲット  : $targetScript" -ForegroundColor Gray
if ($SingleTask) {
    $singleModeLabel = if ($Unreg) { "SingleTask + Unreg（両DB未登録トラックのみ）" } else { "SingleTask（CUEトラック直列・完了待機）" }
    Write-Host " 🧵 実行モード  : $singleModeLabel" -ForegroundColor Gray
} else {
    Write-Host " ⚡ 並列投下数  : $Concurrency スレッド" -ForegroundColor Gray
}
Write-Host "=========================================" -ForegroundColor DarkGray

# Orchestratorの起動チェックと自動起動 (Phase 1: Init Gray/Yellow Warning)
$syncScript = Join-Path $PSScriptRoot "sync_pg_completed_to_sqlite.py"
$extractScript = Join-Path $PSScriptRoot "extract_unanalyzed_flacs.py"
$pythonExe = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path -LiteralPath $pythonExe)) {
    $pythonCommand = Get-Command python.exe -ErrorAction SilentlyContinue
    if ($pythonCommand) { $pythonExe = $pythonCommand.Source }
}

if (-not $NoSync -and -not $Test -and -not $UnanalyzedOnly -and [string]::IsNullOrWhiteSpace($InputList)) {
    if (-not (Test-Path -LiteralPath $pythonExe)) {
        Write-Host "❌ Pythonが見つかりません。同期を実行できません。" -ForegroundColor Red
        exit 1
    }
    if ($DryRun) {
        Write-Host "[DryRun] $pythonExe $syncScript" -ForegroundColor Gray
    } else {
        Write-Host "🔄 PostgreSQL解析済み曲リストをSQLiteへ同期します..." -ForegroundColor Cyan
        & $pythonExe $syncScript
        if ($LASTEXITCODE -ne 0) {
            Write-Host "❌ PostgreSQL → SQLite 同期に失敗したため、バッチを中止します。" -ForegroundColor Red
            exit 1
        }
    }
} elseif ($UnanalyzedOnly -and -not $NoSync) {
    Write-Host "ℹ️ -UnanalyzedOnly 指定のため事前の全件 SQLite 同期をスキップします（PostgreSQL直接差分抽出）。" -ForegroundColor DarkGray
}

$orchestratorProcess = Get-Process -Name "orchestrator", "single-orchestrator" -ErrorAction SilentlyContinue
$orchestratorExe = Join-Path $PSScriptRoot "orchestrator.exe"
if (-not (Test-Path -LiteralPath $orchestratorExe)) {
    $orchestratorExe = Join-Path $PSScriptRoot "orchestrator\orchestrator.exe"
}

if ($SingleTask) {
	$orchestratorExe = Join-Path $PSScriptRoot "single-orchestrator.exe"
    if ($orchestratorProcess -and -not $DryRun) {
        Write-Host "❌ SingleTask は常駐 orchestrator 停止後に実行してください（PID: $($orchestratorProcess.Id -join ', ')）。" -ForegroundColor Red
        exit 1
    }
    if (-not (Test-Path -LiteralPath $orchestratorExe) -and -not $DryRun) {
        Write-Host "❌ single-orchestrator.exe が見つかりません。init.bat で更新版をビルドしてください。" -ForegroundColor Red
        exit 1
    }
} elseif (-not $orchestratorProcess) {
    if ($DryRun) {
        Write-Host "[DryRun] Orchestrator 未起動のため自動起動をスキップします。" -ForegroundColor Gray
    } else {
        Write-Host "⚠️ Orchestrator が起動していらっしゃらないため、自動起動いたしますわ！" -ForegroundColor DarkYellow
        if (Test-Path -LiteralPath $orchestratorExe) {
            Start-Process -FilePath $orchestratorExe -WorkingDirectory $PSScriptRoot
            Start-Sleep -Seconds 2 # 起動を少し待ちますわ
        } else {
            Write-Host "❌ 致命的エラー: orchestrator.exe が見つかりませんわ！init.bat を実行してビルドなさってくださいませ。" -ForegroundColor Red
        }
    }
} else {
    Write-Host "🔵 Phase 1: Orchestrator は既に起動済みでございますわ！" -ForegroundColor Blue
}

# Phase 2: ファイル走査モードの判定 (入力リスト / PG未解析優先抽出 / 単一ファイル / fd / rg / 標準走査)
$flacPaths = @()
$targetReasons = @{}

if (-not [string]::IsNullOrWhiteSpace($InputList)) {
    Write-Host "📋 [Phase 2] 入力リストから対象ファイルを読み込みます: $InputList" -ForegroundColor Cyan
    $flacPaths = @(Get-Content -LiteralPath $InputList -Encoding utf8 | ForEach-Object { $_.Trim() } | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
} elseif ($UnanalyzedOnly) {
    if (-not (Test-Path -LiteralPath $pythonExe)) {
        Write-Host "❌ Pythonが見つかりません。PostgreSQL 抽出を実行できません。" -ForegroundColor Red
        exit 1
    }
    if (-not (Test-Path -LiteralPath $extractScript)) {
        Write-Host "❌ 抽出スクリプトが見つかりません: $extractScript" -ForegroundColor Red
        exit 1
    }
    Write-Host "🔍 [Phase 2] PostgreSQL とディスクを照合し、未解析/空データファイルを優先抽出します..." -ForegroundColor Cyan
    $resolvedRoots = @($MusicRoot | ForEach-Object {
        if (Test-Path -LiteralPath $_) { (Resolve-Path -LiteralPath $_).Path } else { (Resolve-Path -Path $_).Path }
    })
    $extractArgs = @($extractScript, "--roots") + $resolvedRoots + @("--priority", $PriorityOrder, "--format", "json")
    if (-not [string]::IsNullOrWhiteSpace($OutputList) -and -not $DryRun) {
        $extractArgs += @("--output", $OutputList)
    }
    $extractRaw = & $pythonExe @extractArgs
    if ($LASTEXITCODE -ne 0) {
        Write-Host "❌ PostgreSQL 未解析ファイル抽出に失敗したため、バッチを中止します。" -ForegroundColor Red
        exit 1
    }
    $extractResult = $extractRaw | ConvertFrom-Json
    Write-Host (" 📊 抽出結果: ディスク={0} 件 / PG登録ファイル={1} 件 → 優先スキャン対象={2} 件 (PG未登録={3}, 空データ={4})" -f $extractResult.disk_files, $extractResult.pg_files, $extractResult.total_targets, $extractResult.missing_count, $extractResult.empty_count) -ForegroundColor Green
    if (-not [string]::IsNullOrWhiteSpace($OutputList)) {
        if ($DryRun) {
            Write-Host " [DryRun] 抽出リスト保存スキップ: $OutputList" -ForegroundColor Gray
        } else {
            Write-Host " 💾 抽出リスト保存先: $OutputList" -ForegroundColor Gray
        }
    }
    $flacPaths = @($extractResult.targets | ForEach-Object {
        $targetReasons[$_.path] = $_.reason
        $_.path
    })
} else {
    $resolvedRoots = @($MusicRoot | ForEach-Object {
        if (Test-Path -LiteralPath $_) { (Resolve-Path -LiteralPath $_).Path } else { (Resolve-Path -Path $_).Path }
    })

    if ($isSingleFile) {
        Write-Host "🎯 [Phase 2] 単一ファイル直接指定モードですわ！" -ForegroundColor Cyan
        $flacPaths = @($resolvedRoots[0])
    } else {
        $fdCmd = Get-Command fd.exe -ErrorAction SilentlyContinue
        $rgCmd = Get-Command rg.exe -ErrorAction SilentlyContinue

        if ($fdCmd) {
            Write-Host "🦀⚡ [Phase 2] Rust高速モード(fd)起動ですわ！" -ForegroundColor Cyan
            $flacPaths = @($resolvedRoots | ForEach-Object { fd.exe -I -e flac -t f -a --search-path $_ })
        } elseif ($rgCmd) {
            Write-Host "🦀⚡ [Phase 2] Rust高速モード(rg)起動ですわ！" -ForegroundColor Cyan
            $flacPaths = @($resolvedRoots | ForEach-Object { rg.exe --no-ignore --files -g "*.flac" $_ })
        } else {
            Write-Host "🐢 [Phase 2] PowerShell標準フォールバックモードで走査いたしますわ..." -ForegroundColor DarkYellow
            $flacPaths = @($resolvedRoots | ForEach-Object { [System.IO.Directory]::EnumerateFiles($_, "*.flac", [System.IO.SearchOption]::AllDirectories) })
        }
    }
}

if ($flacPaths.Count -eq 0) {
    Write-Host "✅ スキャン対象の FLAC ファイルは 0 件でした。終了いたしますわ。" -ForegroundColor Green
    exit 0
}

$effectiveConcurrency = if ($isSingleFile -or $Concurrency -le 1) { 1 } else { $Concurrency }
$forceBool = $Force.IsPresent
$dryRunBool = $DryRun.IsPresent

# Phase 3: 単発同期実行、または並列キュー投下
$singleFailures = 0
if ($SingleTask) {
    foreach ($flacPath in $flacPaths) {
        if ([string]::IsNullOrWhiteSpace($flacPath)) { continue }
        $idx = [BatchCounter]::Next()
        if (-not $dryRunBool) {
            $running = Get-Process -Name "orchestrator", "single-orchestrator" -ErrorAction SilentlyContinue
            if ($running) {
                Write-Host "[$idx] ❌ 常駐 orchestrator を検出したため中断します（PID: $($running.Id -join ', ')）。" -ForegroundColor Red
                $singleFailures++
                break
            }
        }
        $singleArgs = @("-single-file", $flacPath)
        if ($forceBool) { $singleArgs += "-force" }
        if ($Unreg) { $singleArgs += "-unreg" }
        if ($dryRunBool -and $Unreg) { $singleArgs += "-check-only" }
        if ($dryRunBool -and -not $Unreg) {
            $reasonTag = if ($targetReasons.ContainsKey($flacPath)) { " [$($targetReasons[$flacPath])]" } else { "" }
            Write-Host "[$idx] [DryRun]$reasonTag $orchestratorExe $($singleArgs -join ' ')" -ForegroundColor Gray
            continue
        }
        & $orchestratorExe @singleArgs
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[$idx] ❌ 単発解析失敗 (Exit $LASTEXITCODE): $flacPath" -ForegroundColor Red
            $singleFailures++
        } elseif ($dryRunBool -and $Unreg) {
            Write-Host "[$idx] ✅ 未登録照合完了（解析・DB更新なし）: $flacPath" -ForegroundColor Green
        } else {
            Write-Host "[$idx] ✅ 単発解析完了: $flacPath" -ForegroundColor Green
        }
    }
} elseif ($dryRunBool) {
    foreach ($flacPath in $flacPaths) {
        if ([string]::IsNullOrWhiteSpace($flacPath)) { continue }
        $idx = [BatchCounter]::Next()
        $reasonTag = if ($targetReasons.ContainsKey($flacPath)) { " [$($targetReasons[$flacPath])]" } else { "" }
        Write-Host "[$idx] [DryRun]$reasonTag 実行予定コマンド: POST http://127.0.0.1:8080/task (Target: $flacPath)" -ForegroundColor Gray
    }
} else {
$flacPaths | ForEach-Object -ThrottleLimit $effectiveConcurrency -Parallel {
    $flacPath = $_
    if ([string]::IsNullOrWhiteSpace($flacPath)) { return }
    
    $idx = [BatchCounter]::Next()

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
}

Write-Host ""
Write-Host "=========================================" -ForegroundColor Magenta
if ($SingleTask) {
    Write-Host " 🎉 SingleTask バッチが終了しました（失敗: $singleFailures 件）" -ForegroundColor DarkMagenta
    Write-Host " 📊 合計処理数  : $([BatchCounter]::GetTotal()) 件" -ForegroundColor White
} else {
    Write-Host " 🎉 バッチ処理(タスク投下)が無事に終了いたしましたわ！" -ForegroundColor DarkMagenta
    Write-Host " 📊 合計投下数  : $([BatchCounter]::GetTotal()) 件" -ForegroundColor White
}
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

if ($SingleTask -and $singleFailures -gt 0) {
    exit 1
}
