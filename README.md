# Flac_Analyzer

## 概要

**Flac_Analyzer** は、FLAC形式の音楽ファイル（CUEシートによるインデックス分割を含む）から高精度な音響特徴量（BPM、音量、周波数スペクトル、時系列変化など）を抽出し、AIモデル（ONNX / Essentia）によってジャンルやムードを自動分類・データベース永続化するシステムです。

Windows環境における大量の音楽ライブラリ（50GB〜数TB規模）の一括バッチ処理に最適化されており、以下の技術的アプローチによりメモリ不足（OOM）やDB処理遅延を根絶しています。

- **Go言語による並行ジョブ管理**: ディスパッチャがシステムリソース（空き物理メモリ・`MaxRamRatio` 基準のリアルタイムバックプレッシャー・CPUコア数）を常時監視し、ワーカープロセスの並列実行数を最適制御。`0` 設定時には `runtime.NumCPU()` から全自動スケール。
- **ONNX SegFault 防止 ＆ 3段ワーカー並列実行**: 重い音源分離（Demucs）は `demucs_concurrent_limit = 1` セマフォで排他実行して ONNX Runtime の SegFault をゼロ防止。Demucs 完了後は Freeze（`PAGE_READONLY`）された共有メモリから `Librosa`・`Tensor`・`Essentia` ワーカーを `sync.WaitGroup` により **3本同時に並列実行** し、全CPUコア（全32スレッド）を100%フル稼働。
- **Windows共有メモリ（Shared Memory）WORM転送**: Pythonワーカーでデコード・波形分離した巨大な波形データを、書き込み不可 (`PAGE_READONLY`) に保護した共有メモリ領域でやり取りし、プロセス間の不要なデータコピーやメモリ断片化を根絶。
- **事前ハッシュ比較による高速スキップ**: デコード音源のMD5ハッシュを抽出し、PostgreSQL内の既存レコードと照合することで、解析済みの楽曲に対する重い音源分離（Demucs）や特徴量抽出処理を100%スキップ。
- **PostgreSQL JSONB 永続化と DLQ フォールバック**: 抽出結果を JSONB フォーマットで非同期 UPSERT。データベース障害時はローカル SQLite (`send_failed.db`) に一時退避（Dead Letter Queue）し、復旧後に安全に再送。
- **ゾンビタスクの自動検知・リセット**: オーケストレーター起動時に、前回クラッシュ等でステータスが `RUNNING` / `PENDING` のまま残ったタスクを自動検知して `FAILED` に安全リセットし、誤スキップを防止。
- **一時キャッシュ自動クリーンアップ**: 共有メモリ波形分離および中間データ処理時のキャッシュ（`flac_analyzer_cache`）をタスク完了時・DLQ退避時に完全削除し、RAMディスクやストレージの枯渇を絶滅。
- **タイムスタンプ保護（Timestamp Preservation）**: 解析結果の一部を FLAC タグ (VorbisComment) に書き戻す際、ファイルの各種タイムスタンプ（作成日時・更新日時）を取得し、寸分違わず完全に復元。

---

## 必要なもの

### 1. 動作環境
- **OS**: Windows 11 / Windows 10 (64-bit)
- **Python**: Python 3.12 または 3.13（`.venv` 仮想環境を推奨）
- **Go**: Go 1.22 以上（Go オーケストレーターのビルド用。コンパイル済みバイナリ `orchestrator.exe` を直接使用も可能）
- **PostgreSQL**: PostgreSQL 14 以上（解析データの保存先）

### 2. PostgreSQL データベース要件
事前に PostgreSQL サーバー上でデータベースを作成し、付属の DDL スクリプトを実行してスキーマとアクセスロールを初期化してください。

```bash
# PostgreSQL にログイン後、データベースを作成
CREATE DATABASE flac_analyzer_db;

# 初期化スクリプトを実行してテーブル・トリガー・ロールを適用
psql -d flac_analyzer_db -f sql/schema.sql
```

---

## 使い方 (USAGE)

### 1. 環境構築
Python 仮想環境を作成し、必要な依存ライブラリをインストールします。

```powershell
# 仮想環境の作成と有効化
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1

# パッケージのインストール
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

#### 💡 GPU 加速 (NVIDIA CUDA / DirectML) のセットアップ
推論・音源分離処理を GPU で高速化する場合、環境に合わせてパッケージを選択してください。

- **NVIDIA GPU (CUDA) を使用する場合**:
  ```powershell
  pip uninstall onnxruntime onnxruntime-directml
  pip install onnxruntime-gpu
  pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  ```
- **AMD / Intel iGPU などの DirectX 12 (DirectML) を使用する場合**:
  ```powershell
  pip uninstall onnxruntime onnxruntime-gpu
  pip install onnxruntime-directml
  ```

### 2. 設定ファイルの準備
プロジェクトルートの `config.toml` に PostgreSQL の接続情報や並列実行数、各種スキップフラグを設定します。

```toml
[database]
url = "postgres://username:password@hostname:5432/flac_analyzer_db"

[orchestrator]
num_workers = 4
demucs_concurrent_limit = 1
shm_allocation_delay_sec = 2
queue_dir = "../queue"
skip_dup_by_hash = true
```

#### ⚙️ 設定パラメータ詳細仕様

| パラメータ | 型 | 説明 |
| :--- | :--- | :--- |
| `database.url` | String | PostgreSQL の接続 URI (`postgres://user:pass@host:port/dbname`)。 |
| `orchestrator.num_workers` | Integer | ディスパッチャが同時に並列実行を許可する最大ワーカープロセス数。`0` を指定した場合、システム物理 RAM 容量（`max_ram_ratio` 基準）および CPU 論理コア数（`runtime.NumCPU()`）から安全な最大並列ワーカー数を自動決定します。 |
| `orchestrator.max_ram_ratio` | Float | システム全体物理 RAM に対する使用許可割合（デフォルト: `0.625` ＝ 64GB 環境で約 40GB）。タスク投入時にリアルタイム空き RAM を監視し、この上限を超えると自動的にバックプレッシャー（スロットリング待機）がかかります。 |
| `orchestrator.cpu_worker_ratio` | Float | ワーカー数自動計算時の CPU コア利用率割合（デフォルト: `0.80`）。 |
| `orchestrator.demucs_concurrent_limit` | Integer | 重い GPU/CPU 負荷および ONNX Runtime の SegFault 防止を伴う音源分離 (`worker_demucs.py`) の最大同時実行制限セマフォ（`1` 固定を推奨）。 |
| `orchestrator.shm_allocation_delay_sec` | Integer | 共有メモリ (SHM) の確保・解放タイミングにおけるプロセス間同期用の安全遅延（秒）。 |
| `orchestrator.queue_dir` | String | 各抽出ワーカーが一時的に成果物 JSON を書き出すキューディレクトリのパス。 |
| `orchestrator.skip_dup_by_hash` | Boolean | `true` の場合、音源分離の前に波形 MD5 ハッシュを算出（`--check-hash-only`）し、PostgreSQL に同ハッシュの解析成果が既に存在すれば Demucs 音源分離および特徴量抽出処理を **100% スキップ** します。 |

#### 🔄 `force: true` (再解析フラグ `-Force`) の挙動
`.\run_batch.ps1 -Force` または POST API に `force: true` を指定してリクエストを送信した場合：
1. `orchestrator.db` 上の既存タスク状態 (`COMPLETED`) および `skip_dup_by_hash` による PostgreSQL 重複チェックが **完全に無効化** されます。
2. 過去に解析・永続化済みの楽曲であっても、Demucs による波形分離から全特徴量抽出（Librosa, Tensor, Essentia）、および PostgreSQL への `JSONB` UPSERT（履歴テーブル `raw_library_flac_history` への自動退避含む）が強制再実行されます。
3. アルゴリズムの変更（ Hann 窓適用など）やモデル刷新時に過去データを一括更新する際に使用します。

### 3. 解析モデルの配置
`models/` ディレクトリに必要な ONNX 分類器モデルおよびクラスマッピング JSON を配置します（例: `discogs-effnet-bs64-1.onnx` 等）。

### 4. 実行手順

#### ステップ 1: Go オーケストレーターの起動
`orchestrator` ディレクトリでプログラムを起動します（HTTP API: ポート `8080` / Prometheus メトリクス: ポート `2112`）。

```powershell
cd orchestrator
go build -o orchestrator.exe
.\orchestrator.exe
```

#### ステップ 2: 解析リクエストの送信（一括ディレクトリ走査）
別ウィンドウの PowerShell からディレクトリ走査スクリプトを実行します。

```powershell
# 通常の一括解析 (重複ハッシュスキップ有効)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library"

# 失敗/スキップされたファイルを強制再解析する場合 (-Force フラグ)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library" -Force
```

#### ステップ 3: 失敗タスク（DLQ）の再送処理
PostgreSQL 送信失敗により `send_failed.db` へ一時退避（Dead Letter Queue）されたデータを手動で再送信・DB同期します。

```powershell
.venv\Scripts\python.exe retry_ingest.py
```

> [!NOTE]
> **Tensor特徴量抽出（STFT Hann窓適用）に関する計算結果変更の注意点**
> `worker_tensor.py` の STFT 計算にて `torch.hann_window` を明示指定したことで、従来の矩形窓で発生していたスペクトル漏れ（Spectral Leakage）が解消され、Spectral Flux 等の算出精度が向上・補正されています。旧バージョンで解析済みの楽曲と数値結果がわずかに異なる場合があります。必要に応じて `.\run_batch.ps1 -Force` を使用して再解析を行ってください。

---

## 状態図 (State Diagram)

Go オーケストレーターと Python ワーカープロセス群によるタスク処理の流れを示す状態遷移図です。

```mermaid
stateDiagram-v2
    [*] --> StartupReset: オーケストレーター起動
    StartupReset --> Idle: orchestrator.db の RUNNING/PENDING タスクを FAILED へリセット
    
    Idle --> TaskReceived: /task APIへファイルパスがPOSTされる
    TaskReceived --> CueInspect: worker_cue.py 起動<br/>（CUE/タグ解析・トラック自動抽出）
    CueInspect --> CheckState: orchestrator.db (SQLite) で各トラックの(file_path, track_number)確認
    
    CheckState --> Skipped: 全トラックが COMPLETED / RUNNING / PENDING (force:false 時)
    CheckState --> Queued: 未処理トラックを PENDING として登録
    
    Skipped --> [*]: レスポンス 200 OK (処理スキップ)
    Queued --> ResponseAccepted: レスポンス 202 Accepted (展開トラック数返却)
    ResponseAccepted --> Dispatcher_Loop
    
    state Dispatcher_Loop {
        CalcHash: worker_demucs.py --check-hash-only<br/>(トラック波形MD5算出)
        CalcHash --> CheckHashDB: ingester.py --check-hash<br/>(PostgreSQL重複照合)
        CheckHashDB --> SkippedByHash: PostgreSQLに同ハッシュが既に存在 (skip_dup_by_hash=true 時)
        CheckHashDB --> ResourceWait: 未登録楽曲
        
        ResourceWait --> AllocatingSHM: メモリ空き容量・並列上限セマフォ監視
        AllocatingSHM --> DemucsProcessing: worker_demucs.py 起動<br/>（波形スライスデコード・分離・SHM書き込み）
        DemucsProcessing --> FreezingSHM: Go側で共有メモリを PAGE_READONLY 化
        FreezingSHM --> Precache: functor_precache.py 起動<br/>（SHM read-only アタッチ・メタデータ整合性検証）
        Precache --> ParallelFeatureExtracting: ポストDemucs並列特徴量抽出起動<br/>（Librosa, Tensor, Essentia 3本同時並列実行）
        ParallelFeatureExtracting --> ReleaseSHM: Go側で共有メモリ (SHM) 解放
        ReleaseSHM --> WriteJSONFiles: 中間JSONファイル書き込み<br/>(queue/ ディレクトリへ一時出力)
        WriteJSONFiles --> Ingesting: ingester.py 起動（JSON集約・DB照合）
    }
    
    SkippedByHash --> TaskCompleted: スキップ完了 (status: COMPLETED)
    Ingesting --> PostgreSQL_Upsert: DB正常時 (PostgreSQLへUPSERT)
    Ingesting --> DLQ_Fallback: DB接続不可時 (send_failed.dbへ退避)
    
    PostgreSQL_Upsert --> TagWriteback: FLACタグ書き戻し &<br/>Windows タイムスタンプ保護 (SetFileTime)
    TagWriteback --> IngesterCleanup: ingester.py による中間JSON・キャッシュ削除
    DLQ_Fallback --> IngesterCleanup: 退避後に中間JSON・キャッシュ削除
    
    IngesterCleanup --> TaskCompleted: Go defer クリーンアップ実行後<br/>orchestrator.db の status を COMPLETED に更新
    TaskCompleted --> [*]
```

---

## ER図とデータ構造

### 1. ER図 (Entity Relationship Diagram)

```mermaid
erDiagram
    %% PostgreSQL Tables
    raw_library_flac ||--o{ raw_library_flac_history : "BEFORE UPDATE (Trigger)"
    
    raw_library_flac {
        int id PK "主キー (SERIAL)"
        string audio_hash UK "波形デコードデータのMD5 (32文字)"
        string filepath "ファイル絶対パス"
        string filename "ファイル名"
        int track_number "トラック番号"
        string album_artist "アルバムアーティスト (検索用平坦化)"
        string album "アルバム名 (検索用平坦化)"
        string artist "トラックアーティスト (検索用平坦化)"
        string title "トラックタイトル (検索用平坦化)"
        jsonb meta "FLACタグ等の元メタデータ"
        jsonb features "Librosa等の音響特徴量"
        jsonb predictions "EssentiaによるAI予測スコア"
        timestamp collected_at "レコード収集日時"
        timestamp analyzed_at "解析実行日時"
    }

    raw_library_flac_history {
        int history_id PK "履歴主キー"
        int library_id FK "メインテーブル参照"
        string audio_hash
        string filepath
        string filename
        int track_number
        string album_artist
        string album
        string artist
        string title
        jsonb meta
        jsonb features
        jsonb predictions
        timestamp collected_at
        timestamp analyzed_at
        timestamp archived_at "履歴退避日時"
    }

    %% SQLite Tables (Orchestrator State: orchestrator.db)
    task_state {
        string file_path PK "ファイル絶対パス"
        string status "タスク状態 (PENDING / RUNNING / COMPLETED / FAILED)"
        string error_message "エラーログ詳細"
        datetime updated_at "更新日時"
    }

    %% SQLite Tables (Dead Letter Queue: send_failed.db)
    failed_payloads {
        string audio_hash PK "波形MD5"
        string filepath "ファイル絶対パス"
        string filename "ファイル名"
        int track_number "トラック番号"
        string album_artist
        string album
        string artist
        string title
        json meta "退避メタデータ"
        json features "退避特徴量データ"
        json predictions "退避予測データ"
        datetime failed_at "送信失敗日時"
    }
```

### 2. JSONB データ構造仕様

`raw.library_flac` の `JSONB` カラムに格納される具体的なデータフォーマットです。

#### `meta` カラム (元タグ・CUEシート情報)
```json
{
  "album": "Album Title",
  "artist": "Artist Name",
  "title": "Track Title",
  "date": "2024-01-01",
  "tracknumber": "01",
  "genre": ["Electronic", "Synthwave"],
  "albumartist": "Various Artists",
  "cuesheet": "FILE \"sample.flac\" WAVE ..."
}
```

#### `features` カラム (音響特徴量)
```json
{
  "mix": {
    "scalars": {
      "bpm": 128.0,
      "rms_mean": 0.153,
      "rms_std": 0.045,
      "energy": 45.2,
      "spectral_centroid_mean": 2500.5,
      "zcr_mean": 0.052,
      "hnr_nap": 0.825
    },
    "sequences": {
      "rms": [0.08, 0.12, 0.15, "...(固定32要素)"],
      "spectral_centroid": [1200.0, 1500.0, "..."],
      "mfcc": [
        [-120.0, -115.0, "..."],
        [40.0, 42.0, "..."]
      ]
    }
  },
  "bass": {
    "scalars": {
      "rms_mean": 0.081,
      "spectral_centroid_mean": 450.2
    }
  }
}
```

#### `predictions` カラム (AIモデル予測スコア)
```json
{
  "danceability": 852,
  "tonal_atonal": 910,
  "mood_happy": 720,
  "mood_sad": 105,
  "genre_rosamerica": {
    "house": 800,
    "techno": 150,
    "classical": 50
  }
}
```

---

## Windows 共有メモリ (SHM) 管理と WORM アーキテクチャ

本システムでは、Windows 環境において大量の音源ファイル（数十GB〜数TB）を一括処理する際のメモリ不足（OOM）や I/O ボトルネックを根絶するため、**Windows 共有メモリ (Shared Memory) による WORM (Write-Once Read-Many) アーキテクチャ** を採用しています。

### 1. WORM (Write-Once Read-Many) アーキテクチャ

1. **書き込みフェーズ (Write Phase)**:
   - `worker_demucs.py` が FLAC ファイルをスライスデコードし、Demucs による音源分離 (stems: `mix`, `drums`, `bass`, `other`, `vocals`) を実行します。
   - 分離された float32 多次元配列テンソルは、Windows Win32 API (`CreateFileMappingW`, `MapViewOfFile`) を介して `PAGE_READWRITE` モードでメモリ上に作成された命名共有メモリ領域に直接書き込まれます。
2. **フリーズフェーズ (Freeze Phase)**:
   - Go オーケストレーターが Python プロセスからの書き込み完了を検知すると、共有メモリのメモリ保護属性を `PAGE_READWRITE` から **`PAGE_READONLY`** へ変更（フリーズ）します。
3. **並行読み取りフェーズ (Read-Many Phase)**:
   - 後続の特徴量抽出ワーカー (`functor_precache.py`, `worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py`) は、`PAGE_READONLY` で保護された共有メモリ領域にアタッチします。
   - `functor_precache.py` は、ディスクへの中間 `.npy` ファイル保存を完全に排除し、共有メモリのアタッチ性検証とメタデータ整合性の高速チェックのみを行います。
   - 各抽出ワーカーは、他のワーカーや自身の誤動作によって共有メモリ上の波形データが改変されるリスクから物理的に保護された状態で並行解析を実行します。

### 2. ライフサイクル管理とリーク防止メカニズム

- **Win32 API による精密制御**:
  - Go 側では `syscall` または Win32 DLL 経由で `CreateFileMappingW`, `MapViewOfFile`, `VirtualProtect`, `UnmapViewOfFile`, `CloseHandle` を直接呼出して管理します。
- **同期遅延 (`shm_allocation_delay_sec`)**:
  - 各ワーカーが共有メモリハンドルを閉じる際、OS 側のハンドルフラグクリア待ちによる競合を防ぐため、`shm_allocation_delay_sec` で指定された安全セマフォ遅延が挿入されます。
- **`defer` ステートメントによるガラガラぽん解放**:
  - 全ての特徴量抽出タスク完了時、または途中でエラー（例外やワーカー異常終了）が発生した場合でも、Go の `defer` クリーンアップ関数が確実に発動し、`UnmapViewOfFile` および `CloseHandle` を実行して共有メモリ領域を即座にOSへ返還します。

---

## ライセンス (License)

本プロジェクトのソースコードは [MIT License](file:///a:/Users/letwir/repo/flac_analyzer_forwin/LICENSE) のもとで公開されています。

> [!WARNING]
> **学習済みモデル (ONNX) のライセンスに関する注意点**
> 本リポジトリには AI モデルの重みファイル（ONNX 等）は同梱されていません。
> 本ツールで使用・自動ダウンロードされる外部モデル（Essentia の ONNX 分類器モデル、Discogs 分類器等）には、配布元（MTG / Music Technology Group UPF）の **AGPLv3** や Creative Commons などのライセンスが適用されている場合があります。
> モデルファイルの再配布や商用利用を行う際は、使用する各モデルのライセンス条項を必ずご確認ください。

---
---

# Flac_Analyzer (English)

## Overview

**Flac_Analyzer** is a high-performance audio feature extraction and AI classification system for FLAC music files (including CUE sheet indexing). It extracts detailed acoustic features (BPM, volume dynamics, spectral descriptors, time-series sequences) and automatically classifies genres and moods using ONNX / Essentia AI models, persisting all results to PostgreSQL.

Optimized for processing large-scale music libraries (from 50GB up to multi-terabyte collections) on Windows without Out-Of-Memory (OOM) crashes or database bottlenecking:

- **Go-based Concurrent Job Management**: A Go dispatcher monitors system resources (free RAM, CPU load) to dynamically regulate parallel Python worker processes.
- **Windows Shared Memory (WORM Transfer)**: Decoded waveform data and separated stems are transferred using Windows Shared Memory, locked to `PAGE_READONLY` (Write-Once Read-Many) to eliminate inter-process memory duplication and fragmentation.
- **Pre-Hash Duplicate Bypass**: Computes MD5 checksums of decoded audio waveforms to query PostgreSQL. Existing tracks skip heavy Demucs stem separation and Librosa extraction entirely.
- **PostgreSQL JSONB & DLQ Fallback**: Extracted data is asynchronously UPSERTed as JSONB documents. In case of database connection failures, payloads drop into a local SQLite Dead Letter Queue (`send_failed.db`) for safe retry upon recovery.
- **Stale Task Auto-Recovery**: On startup, the Go orchestrator automatically detects tasks stuck in `RUNNING` or `PENDING` due to prior crashes or abrupt halts and resets them to `FAILED`, preventing accidental task skipping.
- **Automated Temp Cache Cleanup**: Removes intermediate precache files (`flac_analyzer_cache`) automatically upon task completion or DLQ fallback, preventing RAM disk or storage depletion.
- **Timestamp Preservation**: Accurately preserves and restores file timestamps (CreationTime, LastWriteTime) whenever modifying FLAC tags (VorbisComment).

---

## Requirements

### 1. System Requirements
- **OS**: Windows 11 / Windows 10 (64-bit)
- **Python**: Python 3.12 or 3.13 (Virtual environment `.venv` recommended)
- **Go**: Go 1.22+ (Required for building the orchestrator; pre-compiled `orchestrator.exe` can also be used)
- **PostgreSQL**: PostgreSQL 14+ (Storage target for analytical data)

### 2. PostgreSQL Setup
Create a target database on your PostgreSQL server and execute the provided DDL script to initialize tables, triggers, and roles:

```bash
# Log in to PostgreSQL and create the database
CREATE DATABASE flac_analyzer_db;

# Run the initialization DDL script
psql -d flac_analyzer_db -f sql/schema.sql
```

---

## USAGE

### 1. Installation
Create a Python virtual environment and install the required dependencies:

```powershell
# Create and activate virtual environment
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1

# Install requirements
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

#### 💡 GPU Acceleration (NVIDIA CUDA / DirectML) Setup
To accelerate Demucs stem separation and Essentia ONNX inference via GPU:

- **For NVIDIA GPU (CUDA)**:
  ```powershell
  pip uninstall onnxruntime onnxruntime-directml
  pip install onnxruntime-gpu
  pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  ```
- **For DirectX 12 (DirectML) on AMD / Intel iGPU**:
  ```powershell
  pip uninstall onnxruntime onnxruntime-gpu
  pip install onnxruntime-directml
  ```

### 2. Configuration
Configure PostgreSQL connection details, worker parallelism limits, and skip flags in `config.toml`:

```toml
[database]
url = "postgres://username:password@hostname:5432/flac_analyzer_db"

[orchestrator]
num_workers = 4
demucs_concurrent_limit = 1
shm_allocation_delay_sec = 2
queue_dir = "../queue"
skip_dup_by_hash = true
```

#### ⚙️ Configuration Parameter Specifications

| Parameter | Type | Description |
| :--- | :--- | :--- |
| `database.url` | String | PostgreSQL connection URI (`postgres://user:pass@host:port/dbname`). |
| `orchestrator.num_workers` | Integer | Maximum parallel worker processes permitted simultaneously by the dispatcher. |
| `orchestrator.demucs_concurrent_limit` | Integer | Semaphore limit for high-load audio separation tasks (`worker_demucs.py`). |
| `orchestrator.shm_allocation_delay_sec` | Integer | Safety synchronization delay (seconds) when creating/closing shared memory (SHM) regions. |
| `orchestrator.queue_dir` | String | Path to the queue directory where workers write intermediate JSON output payloads. |
| `orchestrator.skip_dup_by_hash` | Boolean | When set to `true`, computes audio MD5 checksums (`--check-hash-only`) before stem separation and **100% bypasses** Demucs and extraction tasks if identical waveform analysis already exists in PostgreSQL. |

#### 🔄 `force: true` (`-Force` Flag) Behavior
When submitting requests via `.\run_batch.ps1 -Force` or passing `"force": true` in the HTTP API body:
1. Existing task status records (`COMPLETED`) in `orchestrator.db` and PostgreSQL hash duplication checks via `skip_dup_by_hash` are **completely bypassed**.
2. Demucs stem separation, feature extraction (Librosa, Tensor, Essentia), and PostgreSQL `JSONB` UPSERT (including auto-archiving to `raw_library_flac_history`) are forcibly re-executed for all targeted files.
3. Useful when recalculating features after windowing/STFT algorithm updates (e.g., Hann window calibration) or AI model upgrades.

### 3. Model Files
Place required ONNX models and label mapping JSON files inside the `models/` directory (e.g., `discogs-effnet-bs64-1.onnx`).

### 4. Running the Pipeline

#### Step 1: Start Go Orchestrator
Launch the Go orchestrator inside the `orchestrator` directory (HTTP API on port `8080`, Prometheus metrics on port `2112`):

```powershell
cd orchestrator
go build -o orchestrator.exe
.\orchestrator.exe
```

#### Step 2: Dispatch Analysis Tasks
In a separate terminal, execute the PowerShell batch scanner script:

```powershell
# Standard batch scan (Duplicate hash skip enabled)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library"

# Force re-analysis of failed or skipped tracks (-Force flag)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library" -Force
```

#### Step 3: DLQ Retries (Optional)
If PostgreSQL was unreachable during processing, manually retry sending saved payloads from `send_failed.db`:

```powershell
.venv\Scripts\python.exe retry_ingest.py
```

> [!NOTE]
> **Notice Regarding Tensor STFT Window Calibration & Feature Numerical Output**
> `worker_tensor.py` now explicitly applies `torch.hann_window` during STFT processing. This eliminates spectral leakage caused by rectangular windowing, resulting in refined Spectral Flux and Tensor feature outputs. If you wish to re-analyze existing tracks to apply this calibration, run `.\run_batch.ps1 -Force`.

---

## State Diagram

Process flow diagram detailing the interaction between the Go orchestrator and Python workers:

```mermaid
stateDiagram-v2
    [*] --> StartupReset: Orchestrator Startup
    StartupReset --> Idle: Reset stale RUNNING/PENDING tasks in orchestrator.db to FAILED
    
    Idle --> TaskReceived: File path POSTed to /task API
    TaskReceived --> CueInspect: Execute worker_cue.py<br/>(Parse CUE/tags & extract tracks)
    CueInspect --> CheckState: Check orchestrator.db (SQLite) for each (file_path, track_number)
    
    CheckState --> Skipped: All tracks already COMPLETED / RUNNING / PENDING (when force:false)
    CheckState --> Queued: Unprocessed tracks registered as PENDING
    
    Skipped --> [*]: 200 OK (Skipped)
    Queued --> ResponseAccepted: 202 Accepted (Enqueued tracks count)
    ResponseAccepted --> Dispatcher_Loop
    
    state Dispatcher_Loop {
        CalcHash: worker_demucs.py --check-hash-only<br/>(Calculate track waveform MD5)
        CalcHash --> CheckHashDB: ingester.py --check-hash<br/>(Check duplication in PostgreSQL)
        CheckHashDB --> SkippedByHash: Hash already exists in PostgreSQL (when skip_dup_by_hash=true)
        CheckHashDB --> ResourceWait: New track
        
        ResourceWait --> AllocatingSHM: Monitor RAM & concurrency limit semaphore
        AllocatingSHM --> DemucsProcessing: Execute worker_demucs.py<br/>(Slice decode/Separate/SHM Write)
        DemucsProcessing --> FreezingSHM: Go freezes SHM to PAGE_READONLY
        FreezingSHM --> Precache: Execute functor_precache.py<br/>(Validate SHM read-only attach & metadata)
        Precache --> FeatureExtracting: Execute extraction workers<br/>(Librosa → Tensor → Essentia)
        FeatureExtracting --> ReleaseSHM: Go closes & unmaps SHM handles
        ReleaseSHM --> WriteJSONFiles: Write intermediate JSON files<br/>(Save temporarily to queue/ directory)
        WriteJSONFiles --> Ingesting: Execute ingester.py (Aggregate JSON & DB sync)
    }
    
    SkippedByHash --> TaskCompleted: Mark completed (status: COMPLETED)
    Ingesting --> PostgreSQL_Upsert: DB available (PostgreSQL UPSERT)
    Ingesting --> DLQ_Fallback: DB unreachable (Save to send_failed.db)
    
    PostgreSQL_Upsert --> TagWriteback: Writeback FLAC tags &<br/>Protect Windows timestamp (SetFileTime)
    TagWriteback --> IngesterCleanup: ingester.py purges temp JSON & cache files
    DLQ_Fallback --> IngesterCleanup: Purge temp files after DLQ fallback
    
    IngesterCleanup --> TaskCompleted: Go defer cleanup & update status to COMPLETED in orchestrator.db
    TaskCompleted --> [*]
```

---

## ER Diagram & Data Structures

### 1. Entity Relationship Diagram (ERD)

```mermaid
erDiagram
    %% PostgreSQL Tables
    raw_library_flac ||--o{ raw_library_flac_history : "BEFORE UPDATE (Trigger)"
    
    raw_library_flac {
        int id PK "Primary Key (SERIAL)"
        string audio_hash UK "Waveform MD5 (32 chars)"
        string filepath "Absolute File Path"
        string filename "File Name"
        int track_number "Track Number"
        string album_artist "Album Artist (Flattened)"
        string album "Album Title (Flattened)"
        string artist "Track Artist (Flattened)"
        string title "Track Title (Flattened)"
        jsonb meta "Raw Metadata / Tags"
        jsonb features "Extracted Acoustic Features"
        jsonb predictions "AI Predictions"
        timestamp collected_at "Collection Timestamp"
        timestamp analyzed_at "Analysis Timestamp"
    }

    raw_library_flac_history {
        int history_id PK "History Primary Key"
        int library_id FK "Reference to raw_library_flac"
        string audio_hash
        string filepath
        string filename
        int track_number
        string album_artist
        string album
        string artist
        string title
        jsonb meta
        jsonb features
        jsonb predictions
        timestamp collected_at
        timestamp analyzed_at
        timestamp archived_at "Archived Timestamp"
    }

    %% SQLite Tables (Orchestrator State: orchestrator.db)
    task_state {
        string file_path PK "Absolute File Path"
        string status "Status (PENDING / RUNNING / COMPLETED / FAILED)"
        string error_message "Error Log Details"
        datetime updated_at "Update Timestamp"
    }

    %% SQLite Tables (Dead Letter Queue: send_failed.db)
    failed_payloads {
        string audio_hash PK "Waveform MD5"
        string filepath "Absolute File Path"
        string filename "File Name"
        int track_number "Track Number"
        string album_artist
        string album
        string artist
        string title
        json meta "Fallback Metadata"
        json features "Fallback Features"
        json predictions "Fallback Predictions"
        datetime failed_at "Failure Timestamp"
    }
```

### 2. JSONB Schema Examples

Sample structures for `JSONB` columns in `raw.library_flac`:

#### `meta` Column (Raw Tags & Cue Sheet)
```json
{
  "album": "Album Title",
  "artist": "Artist Name",
  "title": "Track Title",
  "date": "2024-01-01",
  "tracknumber": "01",
  "genre": ["Electronic", "Synthwave"],
  "albumartist": "Various Artists",
  "cuesheet": "FILE \"sample.flac\" WAVE ..."
}
```

#### `features` Column (Audio Descriptors)
```json
{
  "mix": {
    "scalars": {
      "bpm": 128.0,
      "rms_mean": 0.153,
      "rms_std": 0.045,
      "energy": 45.2,
      "spectral_centroid_mean": 2500.5,
      "zcr_mean": 0.052,
      "hnr_nap": 0.825
    },
    "sequences": {
      "rms": [0.08, 0.12, 0.15, "...(Fixed 32 elements)"],
      "spectral_centroid": [1200.0, 1500.0, "..."],
      "mfcc": [
        [-120.0, -115.0, "..."],
        [40.0, 42.0, "..."]
      ]
    }
  },
  "bass": {
    "scalars": {
      "rms_mean": 0.081,
      "spectral_centroid_mean": 450.2
    }
  }
}
```

#### `predictions` Column (AI Predictions)
```json
{
  "danceability": 852,
  "tonal_atonal": 910,
  "mood_happy": 720,
  "mood_sad": 105,
  "genre_rosamerica": {
    "house": 800,
    "techno": 150,
    "classical": 50
  }
}
```

---

## Windows Shared Memory (SHM) Management & WORM Architecture

To process massive audio collections (tens of GBs to multi-TBs) without Out-Of-Memory (OOM) crashes or disk I/O bottlenecks on Windows, this system implements a **WORM (Write-Once Read-Many) architecture using Windows Shared Memory (SHM)**.

### 1. WORM (Write-Once Read-Many) Architecture

1. **Write Phase**:
   - `worker_demucs.py` slice-decodes FLAC audio and separates stems (`mix`, `drums`, `bass`, `other`, `vocals`) via Demucs.
   - Separated float32 multi-dimensional array tensors are written directly into named Windows shared memory regions initialized in `PAGE_READWRITE` mode using Win32 APIs (`CreateFileMappingW`, `MapViewOfFile`).
2. **Freeze Phase**:
   - Upon completion of Demucs writing, the Go orchestrator updates the memory protection state of the shared memory mapping from `PAGE_READWRITE` to **`PAGE_READONLY`** (Freeze).
3. **Read-Many Phase**:
   - Downstream extraction workers (`functor_precache.py`, `worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py`) attach to the `PAGE_READONLY` memory region.
   - `functor_precache.py` eliminates all temporary `.npy` disk file writes, performing high-speed read-only attachment validation and metadata verification instead.
   - Extraction workers run feature extraction concurrently while physically protected against memory corruption or accidental array mutation.

### 2. Lifecycle Management & Memory Leak Prevention

- **Win32 API Control**:
  - Direct kernel handle orchestration via Go `syscall` / Win32 DLL calls (`CreateFileMappingW`, `MapViewOfFile`, `VirtualProtect`, `UnmapViewOfFile`, `CloseHandle`).
- **Synchronized Allocation Delay (`shm_allocation_delay_sec`)**:
  - Prevents race conditions during Win32 handle unmapping across multiple subprocesses by applying a configurable delay buffer.
- **Guaranteed Cleanup via Go `defer`**:
  - Whether a task completes successfully or aborts on failure, Go's `defer` cleanup pipeline guarantees that `UnmapViewOfFile` and `CloseHandle` are called, instantly releasing Windows SHM pages back to the system OS pool.

---

## License

The source code of this project is licensed under the [MIT License](file:///a:/Users/letwir/repo/flac_analyzer_forwin/LICENSE).

> [!WARNING]
> **Notice Regarding Pre-trained AI Models (ONNX)**
> This repository contains source code only and does NOT include any pre-trained model weights.
> External models fetched or used by this tool (e.g., Essentia ONNX models, Discogs classifiers) may be subject to their original licensing terms, such as **AGPLv3** (by MTG / Music Technology Group UPF) or Creative Commons licenses.
> Users are responsible for checking and complying with the licensing terms of any third-party models when redistributing or using them for commercial purposes.
