# Flac_Analyzer

🇺🇸 [English version](README_en.md)

## 概要

**Flac_Analyzer** は、FLAC形式の音楽ファイル（CUEシートによるインデックス分割を含む）から高精度な音響特徴量（BPM、音量、周波数スペクトル、時系列変化など）を抽出し、AIモデル（ONNX / Essentia）によってジャンルやムードを自動分類・データベース永続化するシステムです。

Windows環境における大量の音楽ライブラリ（50GB〜数TB規模）の一括バッチ処理に最適化されており、以下の技術的アプローチによりメモリ不足（OOM）やDB処理遅延を根絶しています。

- **Go言語による並行ジョブ管理**: ディスパッチャがシステムリソース（空き物理メモリ・`MaxRamRatio` 基準のリアルタイムバックプレッシャー・CPUコア数）を常時監視し、ワーカープロセスの並列実行数を最適制御。`0` 設定時には `runtime.NumCPU()` から全自動スケール。
- **ONNX SegFault 防止 ＆ 3段ワーカー並列実行**: 重い音源分離（Demucs）は `demucs_concurrent_limit = 1` セマフォで排他実行して ONNX Runtime の SegFault をゼロ防止。Demucs 完了後は Freeze（`PAGE_READONLY`）された共有メモリから `Librosa`・`Tensor`・`Essentia` ワーカーを `sync.WaitGroup` により **3本同時に並列実行** し、全CPUコア（全32スレッド）を100%フル稼働。
- **Windows共有メモリ（Shared Memory）WORM転送**: Pythonワーカーでデコード・波形分離した巨大な波形データを、書き込み不可 (`PAGE_READONLY`) に保護した共有メモリ領域でやり取りし、プロセス間の不要なデータコピーやメモリ断片化を根絶。
- **事前ハッシュ比較による高速スキップ**: デコード音源のMD5ハッシュを抽出し、PostgreSQL内の既存レコードと照合することで、解析済みの楽曲に対する重い音源分離（Demucs）や特徴量抽出処理を100%スキップ。
- **CUE自動パース ＆ CUE無しFLACフォールバック**: CUEシート境界を自動パースしてトラック単位に展開。CUEが存在しない通常FLACファイルやパース失敗時も自動で単一トラック処理へ安全にフォールバック移行。
- **VorbisComment 複数値タグの JSONB リスト完全保存**: `ARTIST` 等のマルチバリュータグを文字列結合で潰すことなく、`meta` (JSONB) カラムへ JSON 配列 (`["...", "..."]`) として完全保持したまま保存。
- **PostgreSQL JSONB 永続化と DLQ フォールバック**: 抽出結果を JSONB フォーマットで非同期 UPSERT。データベース障害時はローカル SQLite (`send_failed.db`) に一時退避（Dead Letter Queue）し、復旧後に安全に再送。
- **ゾンビタスクの自動検知・リセット**: オーケストレーター起動時に、前回クラッシュ等でステータスが `RUNNING` / `PENDING` のまま残ったタスクを自動検知して `FAILED` に安全リセットし、誤スキップを防止。
- **一時キャッシュ自動クリーンアップ**: 共有メモリ波形分離および中間データ処理時のキャッシュ（`flac_analyzer_cache`）をタスク完了時・DLQ退避時に完全削除し、RAMディスクやストレージの枯渇を絶滅。
- **タイムスタンプ保護（Timestamp Preservation）**: 解析結果の一部を FLAC タグ (VorbisComment) に書き戻す際、ファイルの各種タイムスタンプ（作成日時・更新日時）を取得し、寸分違わず完全に復元。

### 📚 ドキュメント一覧
| ドキュメント | 内容 |
|:---|:---|
| [状態遷移図](docs/state_diagram.md) | パイプライン全体の状態遷移フロー |
| [ER図・データ構造](docs/database_er_diagram.md) | PostgreSQL/SQLite テーブル定義・JSONB仕様 |
| [SHM/WORMアーキテクチャ](docs/shm_architecture.md) | 共有メモリ管理・ゼロコピーIPC |
| [CPU並列処理・RAM制御](docs/cpu_parallelism_and_ram_guard.md) | ワーカー並列化・メモリバックプレッシャー |
| [CUEパースフロー](docs/cue_parsing_flow.md) | CUEシート検出・フォールバック判定 |
| [DLQ・エラーリカバリ](docs/dlq_error_recovery.md) | Dead Letter Queue・ゾンビタスクリセット |
| [GPU/RAMフォールバック](docs/gpu_fallback_and_ram_defense.md) | CUDA→CPU自動切替・RAM防御 |

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
詳細なダイアグラムおよび各プロセスの挙動仕様については [状態遷移図 (docs/state_diagram.md)](docs/state_diagram.md) を参照してください。

---

## ER図とデータ構造

PostgreSQL のメインテーブル・歴史履歴テーブル、および SQLite のタスク状態管理・Dead Letter Queue (DLQ) のテーブル構造と JSONB データフォーマット仕様です。
詳細なダイアグラムおよび各種スキーマ仕様については [ER図とデータ構造 (docs/database_er_diagram.md)](docs/database_er_diagram.md) を参照してください。

---

## Windows 共有メモリ (SHM) 管理と WORM アーキテクチャ

Windows 環境における大容量音源ファイルのメモリ枯渇を防ぐ Win32 API 制御および WORM (PAGE_READONLY) 共有メモリ管理アーキテクチャです。
詳細なメカニズムおよびライフサイクル管理仕様については [SHM/WORMアーキテクチャ (docs/shm_architecture.md)](docs/shm_architecture.md) を参照してください。

---

## ライセンス (License)

本プロジェクトのソースコードは [MIT License](LICENSE) のもとで公開されています。

> [!WARNING]
> **学習済みモデル (ONNX) のライセンスに関する注意点**
> 本リポジトリには AI モデルの重みファイル（ONNX 等）は同梱されていません。
> 本ツールで使用・自動ダウンロードされる外部モデル（Essentia の ONNX 分類器モデル、Discogs 分類器等）には、配布元（MTG / Music Technology Group UPF）の **AGPLv3** や Creative Commons などのライセンスが適用されている場合があります。
> モデルファイルの再配布や商用利用を行う際は、使用する各モデルのライセンス条項を必ずご確認ください。
