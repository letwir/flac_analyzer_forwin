# Flac_Analyzer

🇺🇸 [English version](README_en.md) / 🇯🇵 [日本語版](README.md)

## ナニコレ？

FLAC 音楽ファイル（CUE シート分割対応）から Demucs 音源分離と Librosa / Tensor / Essentia の音響特徴量を抽出し、PostgreSQL へ保存する Windows 向け大規模並列解析システムです。
Go オーケストレーターによるリソース監視（Gatekeeper）と Windows 共有メモリ（WORM）ゼロコピー転送により、OOM や DB 待機遅延を防ぎながら高速に処理します。
未登録ファイルや空データの差分抽出スキャン、失敗時ローカル DLQ 退避、FLAC タグへの限定書き戻しとタイムスタンプ保護にも対応しています。

---

## 使い方

### `run_batch.ps1` の基本とよく使うパターン

バッチ投入は PowerShell 7（`pwsh`）で [run_batch.ps1](run_batch.ps1) を実行します（未解析差分抽出時は常駐 `orchestrator.exe` と競合しない `-SingleTask` 併用が推奨です）。

```powershell
# 1. PostgreSQL とディスクを照合し、未登録 + features/predictions 空データだけを抽出して確認 (DryRun)
.\run_batch.ps1 -UnanalyzedOnly -SingleTask -DryRun

# 2. 上記で抽出した未解析・空データだけを優先して順次解析・DB登録（M:\Music\album と N:\Music が既定）
.\run_batch.ps1 -UnanalyzedOnly -SingleTask

# 3. 特定フォルダだけを対象に未解析抽出＋CUEトラック単位の未完了分のみ実行
.\run_batch.ps1 "N:\Music\DLC" -UnanalyzedOnly -SingleTask -Unreg

# 4. 抽出結果をリスト保存してから、別タイミングでそのリストだけをスキャン
.\run_batch.ps1 -UnanalyzedOnly -DryRun  # 事前確認後、実保存は以下
.venv\Scripts\python.exe extract_unanalyzed_flacs.py --output tmp\targets.txt
.\run_batch.ps1 -InputList tmp\targets.txt -SingleTask

# 5. 通常モード（常駐 orchestrator.exe を自動起動/利用して並列投下）
.\run_batch.ps1 -Dir "M:\Music\album"

# 6. 解析済み曲も含めて強制再解析する (-Force)
.\run_batch.ps1 -Dir "M:\Music\album" -Force
```

> [!NOTE]
> `-UnanalyzedOnly` は起動前の全件 SQLite 同期（[sync_pg_completed_to_sqlite.py](sync_pg_completed_to_sqlite.py)）を自動スキップし、PostgreSQL から直接差分抽出します。`-DryRun` 時は `orchestrator.exe` の起動や `-OutputList` のファイル保存も行いません。

### `run_batch.ps1` オプション早見表

| オプション | 既定値 / 型 | 説明 |
| :--- | :--- | :--- |
| `-MusicRoot` (`-Path`, `-Dir`, 第1引数) | `M:\Music\album`（`-UnanalyzedOnly` 未指定時）<br>`M:\Music\album, N:\Music`（`-UnanalyzedOnly` 時） | 走査対象の FLAC ファイルまたはディレクトリ（複数指定可）。 |
| `-Concurrency` (`-c`, 第2引数) | `8` (`int`) | 常駐モード時のタスク投下並列数。 |
| `-UnanalyzedOnly` (`-PgUnanalyzed`, `-PriorityScan`) | `switch` | PG 未登録＋`features`/`predictions` が空のファイルだけを抽出して優先スキャン。 |
| `-PriorityOrder` | `missing-first` | `-UnanalyzedOnly` の並び順（`missing-first` / `empty-first` / `size-asc`）。 |
| `-SingleTask` | `switch` | 常駐 `orchestrator.exe` を使わず `single-orchestrator.exe` で 1 件ずつ同期解析（常駐版停止後に実行）。 |
| `-Unreg` | `switch` | `-SingleTask` 専用。SQLite と PG をトラック単位で厳密照合し、未完了トラックのみ実行（`-Force` と併用不可）。 |
| `-Force` | `switch` | 解析済みトラックも強制再解析（`-Unreg` と併用不可）。 |
| `-DryRun` | `switch` | 解析・DB 書き込み・プロセス起動を行わず実行予定のみ表示。 |
| `-NoSync` | `switch` | 通常バッチ開始前の `sync_pg_completed_to_sqlite.py` をスキップ。 |
| `-InputList` / `-OutputList` | `string` | 事前抽出した UTF-8 パス一覧の読み込み / `-UnanalyzedOnly` 結果の保存先。 |
| `-Test` | `switch` | 一時ディレクトリにダミー構成を作って動作確認。 |

### 日常運用・メンテナンスコマンド

```powershell
# DLQ (send_failed.db) に退避された未送信レコードを PostgreSQL へ再送
.venv\Scripts\python.exe zig\retry_ingest.py

# 空の features = {} レコードを検索 / 修復 (詳細は docs/setup_and_config.md)
.\fix.bat
.\fix.bat --id 89476 --apply

# リアルタイム TUI 進捗ダッシュボード (Prometheus :2112/metrics)
.venv\Scripts\python.exe zig\dashboard.py

# DB ⇔ FLAC タグ整合性チェック / 修復
.venv\Scripts\python.exe zig\check_tag_consistency.py --dir "M:\Music" --mode diff

# テスト＆バイナリ更新ビルド (orchestrator.exe / single-orchestrator.exe)
.\update.bat
```

---

## 初回セッティング方法

1. **動作要件を揃える**: Windows 10/11 (64-bit)、PowerShell 7 (`pwsh`)、Python 3.12 / 3.13、Go 1.22 以上、PostgreSQL 14 以上を用意します（高速走査のため `fd.exe` または `rg.exe` の導入を推奨）。
2. **PostgreSQL を初期化する**: DB を作成し、[sql/schema.sql](sql/schema.sql) を適用します。
   ```bash
   psql -d flac_analyzer_db -f sql/schema.sql
   ```
3. **設定ファイルを作成する**: [config.toml.example](config.toml.example) を `config.toml` にコピーし、`[database].url` や各種上限値を環境に合わせて設定します。
4. **Python 環境構築・モデル取得・Go ビルドを一括実行する**:
   - GPU 別パッケージ（CUDA 12 / Blackwell RTX 50xx / DirectML）や `.venv` を準備したうえで [init.bat](init.bat) を実行すると、モデル取得・ONNX 変換と `orchestrator.exe` / `single-orchestrator.exe` のビルドが一括完了します。
   - GPU 別インストール手順・`config.toml` 全パラメータ一覧・ホットリロード API・`update.bat` / `fix.bat` の詳細は **[初回セッティング・設定詳細ガイド (docs/setup_and_config.md)](docs/setup_and_config.md)** および **[RTX 50xx ガイド (docs/install_blackwell_rtx50.md)](docs/install_blackwell_rtx50.md)** を参照してください。

---

## 概要と Docs リンク

### 技術概要

- **リソース制御と OOM 防止**: 物理 RAM・専用 VRAM・ディスク容量を常時監視する Admission Controller / Gatekeeper と、`EnableVirtualLock`・Disk Mode フォールバックで長尺音源のクラッシュを防ぎます。
- **WORM 共有メモリ & CPU/GPU 並行パイプライン**: Demucs 分離後の 7 ステム波形を `PAGE_READONLY` の Windows 共有メモリに固定し、常駐 `worker_daemon.py`（CPU/GPU レーン分離）からゼロコピー抽出します。
- **耐障害永続化**: DB 接続遅延・障害時はローカル SQLite DLQ（`send_failed.db`）へ退避してワーカーを止めず、CUE 展開・波形 MD5 重複スキップ・FLAC タグ書き戻し時のタイムスタンプ復元を備えます。

### 状態図

Go オーケストレーターと Python ワーカー群のタスク状態遷移の詳細は **[状態遷移図 (docs/state_diagram.md)](docs/state_diagram.md)** を参照してください。

### ER図とデータ構造

PostgreSQL（`raw.library_flac` / `raw.library_flac_history`）および SQLite（`task_state` / `failed_payloads`）のスキーマ仕様は **[ER図・データ構造 (docs/database_er_diagram.md)](docs/database_er_diagram.md)** を参照してください。

### Windows 共有メモリ (SHM) 管理と WORM アーキテクチャ

Win32 共有メモリ確保・`PAGE_READONLY` 保護・ライフサイクル管理の詳細は **[SHM/WORM アーキテクチャ (docs/shm_architecture.md)](docs/shm_architecture.md)** を参照してください。

### 📚 ドキュメント一覧

| ドキュメント | 内容 |
| :--- | :--- |
| [初回セッティング・設定詳細](docs/setup_and_config.md) | GPU 環境構築、`config.toml` 全パラメータ、`update.bat` / `fix.bat` 詳細 |
| [状態遷移図](docs/state_diagram.md) | パイプライン全体の状態遷移フロー |
| [ER図・データ構造](docs/database_er_diagram.md) | PostgreSQL / SQLite テーブル定義・JSONB 仕様 |
| [SHM/WORM アーキテクチャ](docs/shm_architecture.md) | 共有メモリ管理・ゼロコピー IPC |
| [CPU 並列処理・RAM 制御](docs/cpu_parallelism_and_ram_guard.md) | ワーカー並列化・Gatekeeper・RAM 制御 |
| [CUE パースフロー](docs/cue_parsing_flow.md) | CUE シート検出・フォールバック判定 |
| [DLQ・エラーリカバリ](docs/dlq_error_recovery.md) | Dead Letter Queue・ゾンビタスクリセット |
| [GPU / RAM フォールバック](docs/gpu_fallback_and_ram_defense.md) | CUDA / DirectML / Blackwell 対応・VRAM 解放 |
| [Blackwell RTX 50xx インストール](docs/install_blackwell_rtx50.md) | NVIDIA RTX 50xx シリーズ (CUDA 13.2) セットアップ |
| [治具スクリプト集](docs/utility_tools.md) | 独立治具集 (`zig/`) の仕様・使用例 |

---

## ライセンス (License)

本プロジェクトのソースコードは [MIT License](LICENSE) のもとで公開されています。

> [!WARNING]
> **学習済みモデル (ONNX) のライセンスに関する注意点**
> 本リポジトリには AI モデルの重みファイル（ONNX 等）は同梱されていません。
> 使用する外部モデル（Essentia の ONNX 分類器モデル等）のライセンス条項（AGPLv3 / CC 等）を必ずご確認ください。
