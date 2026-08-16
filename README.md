# Flac_Analyzer

🇺🇸 [English version](README_en.md) / 🇯🇵 [日本語版](README.md)

## ナニコレ？

**Flac_Analyzer** は、FLAC音楽ファイル（CUEシート分割対応）から音響特徴量を抽出し、AIでジャンルやムードを自動分類してPostgreSQLへ保存するシステムです。
Windows環境における大規模ライブラリ（50GB〜数TB）のバッチ処理に最適化されており、メモリ不足（OOM）やDB処理遅延を根絶しています。
Goによる並行ジョブ管理とWindows共有メモリ（Shared Memory）WORM転送により、全CPUコアを100%フル稼働させながら高速・安全に解析を行います。

---

## 必要なもの

### 1. 動作環境
- **OS**: Windows 11 / Windows 10 (64-bit)
- **Python**: Python 3.12 または 3.13（`.venv` 仮想環境を推奨）
- **Go**: Go 1.22 以上（Go オーケストレーターのビルド用。付属の `init.bat` またはビルド済み `orchestrator.exe` も利用可能）
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

#### 💡 通常環境 (RTX 30xx / 40xx, DirectML, CPU)
通常の Python 仮想環境を作成し、標準依存ライブラリをインストールします。

```powershell
# 仮想環境の作成と有効化
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1

# パッケージのインストール
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

- **NVIDIA GPU (CUDA 12.x) を使用する場合**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-directml
  pip install onnxruntime-gpu
  pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  ```
- **AMD / Intel iGPU などの DirectX 12 (DirectML) を使用する場合**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-gpu
  pip install onnxruntime-directml
  ```

#### 🚀 NVIDIA RTX 50xx シリーズ (Blackwell アーキテクチャ / CUDA 13.2+)
GeForce RTX 5070 Ti / 5080 / 5090 等の Blackwell 世代 GPU をお使いの場合は、専用ガイドおよび `requirements-blackwell.txt` を使用してください。

```powershell
pip install -r requirements-blackwell.txt
```

> [!TIP]
> 詳細な Blackwell 向け環境構築手順・検証コマンドについては [NVIDIA RTX 50xx 専用セットアップガイド (docs/install_blackwell_rtx50.md)](docs/install_blackwell_rtx50.md) を参照してください。

---

### 2. 設定ファイルの準備
プロジェクトルートの `config.toml` に PostgreSQL の接続情報や並列実行数、各種スキップフラグを設定します。
（リポジトリ内の `config.toml.example` をコピーしてご使用ください）

```toml
[database]
url = "postgres://username:password@hostname:5432/flac_analyzer_db"

[orchestrator]
num_workers = 0  # 0指定でCPUコア数・メモリに基づく全自動スケール
max_ram_ratio = 0.625
demucs_concurrent_limit = 1
shm_allocation_delay_sec = 2
queue_dir = "../queue"
skip_dup_by_hash = true
```

#### ⚙️ 設定パラメータ詳細仕様

| パラメータ | 型 | 説明 |
| :--- | :--- | :--- |
| `database.url` | String | PostgreSQL の接続 URI (`postgres://user:pass@host:port/dbname`)。 |
| `orchestrator.num_workers` | Integer | ディスパッチャが同時に並列実行を許可する最大ワーカープロセス数。`0` を指定した場合、ホスト物理 RAM 容量および CPU 論理コア数から安全な最大並列ワーカー数を自動決定します。 |
| `orchestrator.max_ram_ratio` | Float | システム全体物理 RAM に対する使用許可割合（デフォルト: `0.625`）。リアルタイム空き RAM を監視し、上限を超えると自動的にバックプレッシャー（スロットリング待機）がかかります。 |
| `orchestrator.demucs_concurrent_limit` | Integer | 重い GPU/CPU 負荷および ONNX Runtime の SegFault 防止を伴う音源分離 (`worker_demucs.py`) の最大同時実行制限セマフォ（`1` 固定を推奨）。 |
| `orchestrator.shm_allocation_delay_sec` | Integer | 共有メモリ (SHM) の確保・解放タイミングにおけるプロセス間同期用の安全遅延（秒）。 |
| `orchestrator.queue_dir` | String | 各抽出ワーカーが一時的に成果物 JSON を書き出すキューディレクトリのパス。 |
| `orchestrator.skip_dup_by_hash` | Boolean | `true` の場合、波形 MD5 ハッシュを算出（`--check-hash-only`）し、PostgreSQL に同ハッシュの解析成果が既に存在すれば Demucs 音源分離および特徴量抽出処理を **100% スキップ** します。 |
| `orchestrator.gatekeeper_retry_delay_sec` | Integer | Gatekeeper（メモリ事前判定）の NOGO 判定時におけるタスク再試行待機秒数（デフォルト: `20` 秒）。 |
| `orchestrator.config_watch_interval_sec` | Integer | `config.toml` の変更検知・ホットリロード監視間隔（デフォルト: `600` 秒 = 10分）。 |
| `orchestrator.enable_dlq_retry` | Boolean | PostgreSQL 送信失敗時 DLQ (`send_failed.db`) の自動再送・リカバリ有効化（デフォルト: `true`）。 |
| `orchestrator.dlq_retry_interval_sec` | Integer | DLQ 定期自動再送間隔（秒、デフォルト: `600` 秒 = 10分、`0` で定期実行無効化）。 |

#### 🔄 設定の動的再読み込み（ホットリロード）
Orchestrator は稼働中に **`config.toml` の変更を自動検知して即座に動的反映** します。

- **自動リロード (File Watcher)**: `config_watch_interval_sec`（デフォルト10分）ごとに `config.toml` の変更を自動検知し、稼働中のプロセスを停止することなく設定（`demucs_concurrent_limit`, `log_level`, `max_ram_ratio`, `gatekeeper_retry_delay_sec` 等）が更新されます。
- **手動リロード API**: `POST http://localhost:8080/reload` を呼び出すことで即座にリロードし、変更差分を JSON で取得できます。
- **設定確認 API**: `GET http://localhost:8080/config` で現在適用されている動的設定値を確認できます。

#### 🔄 `force: true` (再解析フラグ `-Force`) の挙動
`.\run_batch.ps1 -Force` を指定して実行した場合、過去に解析・永続化済みの楽曲であっても Demucs 音源分離から全特徴量抽出、PostgreSQL への `JSONB` UPSERT（履歴テーブル `raw_library_flac_history` への自動退避含む）が強制再実行されます。

---

### 3. 解析モデルの配置
`models/` ディレクトリに必要な ONNX 分類器モデルおよびクラスマッピング JSON を配置します（例: `discogs-effnet-bs64-1.onnx` 等）。`init.bat` または `python zig/init_dl_model.py` により自動取得可能です。

---

### 4. 実行手順

#### ステップ 1: ワンタップ初期化 ＆ Go オーケストレーターの起動
プロジェクトルートの `init.bat` を実行することで、**Python仮想環境構築**、**モデルの自動ダウンロード ＆ .pb から .onnx への自己変換**、および **Go オーケストレーターのコンパイル** が一括で自動実行されます。

```powershell
# 方法 A: init.bat によるワンタップ一括初期化 ＆ ビルド
.\init.bat

# 方法 B: 手動ビルド ＆ 起動
cd orchestrator
go build -o orchestrator.exe
.\orchestrator.exe
```

#### ステップ 2: 解析リクエストの送信（一括ディレクトリ走査）
別ウィンドウの PowerShell からディレクトリ走査スクリプトを実行します。高速コマンド (`fd.exe` / `rg.exe`) が利用可能な場合は自動的に Rust 高速走査モードで実行されます。

```powershell
# 通常の一括解析 (重複ハッシュスキップ有効)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library"

# 失敗/スキップされたファイルを強制再解析する場合 (-Force フラグ)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library" -Force
```

#### ステップ 3: 失敗タスク（DLQ）の自動再送・リカバリ
PostgreSQL 送信失敗により `send_failed.db` へ一時退避（Dead Letter Queue）されたデータは、Orchestrator 起動時および定期実行（10分間隔）により自動的に PostgreSQL へ再送・同期されます。手動で即時実行することも可能です。

```powershell
.venv\Scripts\python.exe zig/retry_ingest.py
```

#### ステップ 4: リアルタイム進捗ダッシュボード ＆ Prometheus メトリクス監視
Orchestrator は `:2112/metrics` にて **1ファイルあたりの所要時間**、**1曲（トラック）あたりの所要時間**、**処理速度（files/min, tracks/min）**、**ETA（残り時間）**、**RAM/ディスク空き容量** の Prometheus メトリクスをリアルタイム配信しています。

専用 TUI ダッシュボード治具を実行することで、ターミナル上で美麗に進捗と所要時間をライブ監視できます。

```powershell
# リアルタイム TUI 進捗ダッシュボードの起動
.venv\Scripts\python.exe ./zig/dashboard.py

# 1回のみ現在のステータスを出力して終了
.venv\Scripts\python.exe ./zig/dashboard.py --once
```

#### ステップ 5: DB ⇔ FLAC タグの双方向整合性検査 ＆ 一括修復 (./zig/)
PostgreSQL DB と実 FLAC ファイルの間で、特徴量タグの未反映、メタデータの不一致、未インジェストファイルを双方向で高速クロスチェックし、安全に一括修復します。

```powershell
# 双方向差分検出 (Dry-run モード)
.venv\Scripts\python.exe ./zig/check_tag_consistency.py --dir "M:\Music" --mode diff

# 不足タグの一括修復（FLAC ファイルへの自動書き戻し）
.venv\Scripts\python.exe ./zig/check_tag_consistency.py --dir "M:\Music" --repair

# 差分レポートを JSON ファイルへ保存
.venv\Scripts\python.exe ./zig/check_tag_consistency.py --dir "M:\Music" --output-json diff_report.json
```


---

## 概要詳しく

**Flac_Analyzer** は、以下の最新技術的アプローチによりメモリ不足（OOM）やDB処理遅延を根絶しています。

- **ハードウェア自律検知 ＆ 開発/実行環境動的タグ付け**: オーケストレーター起動時に Go ネイティブ sysinfo (および Win32 CIM スクリプト) がホスト物理 RAM・CPU・GPU を自動検知し、`HARDWARE_SPECS.md` のスペック情報を動的更新。
- **Waveform長ベース Demucs RAM予測 ＆ GO/NOGO ゲートキーパー**: CUE/FLAC の PCM 波形長から Demucs 音源分離に必要な RAM を事前に算定。空き物理 RAM が不足している場合はタスク投入を自動待機（Gatekeeper Decision）し、OOM クラッシュを完全防御。
- **`tensorSemaphore` ＆ VRAM 逐次解放**: ディスパッチャ内に `tensorSemaphore` を配置し、PyTorch ワーカー完了毎に `torch.cuda.empty_cache()` を呼び出して VRAM を逐次クリーンアップ。
- **Go言語による並行ジョブ管理**: ディスパッチャがシステムリソース（空き物理メモリ・`MaxRamRatio` 基準のリアルタイムバックプレッシャー・CPUコア数）を常時監視し、ワーカープロセスの並列実行数を最適制御。
- **ONNX SegFault 防止 ＆ 3段ワーカー並列実行**: Demucs 音源分離は `demucs_concurrent_limit = 1` セマフォで排他実行し ONNX SegFault を予防。完了後は Freeze された共有メモリから `Librosa`・`Tensor`・`Essentia` ワーカーを `sync.WaitGroup` により **3本同時並列実行**。
- **Windows共有メモリ（Shared Memory）WORM転送**: 書き込み不可 (`PAGE_READONLY`) に保護した共有メモリ領域で巨大波形データを共有し、プロセス間コピーやメモリ断片化を絶滅。
- **float32 精度最適化 ＆ 64-bit 暗黙キャスト抑止**: Librosa や Scipy 特徴量抽出において `float32` 直計算および中間精度のハイブリッド保護を適用。25分超の長尺トラックでも Windows ページファイル超過 (WinError 1455) を根絶。
- **`flac_getinfo.py` による読取専有射 (Reader Morphism) 一元化**: FLAC ファイルからの VorbisComment、CUEシート、音声形式情報（SampleRate, Channels, Duration等）の読み取りを単一の純粋モジュールへ分離独立させ、特徴抽出・音源分離処理からのファイルIO副作用依存を完全排除。
- **標準 ANSI 8 色による進捗暗明グラデーション**: ログ出力を「灰 (Dim Gray: 最暗) $\to$ 青 (Blue) $\to$ 紫 (Magenta) $\to$ シアン (Cyan) $\to$ 緑 (Green) $\to$ ボールドブライトホワイト (Bold Bright White: 最光)」へと収束させ、黄・赤・橙を WARN/ERROR 専用に厳格分離して視認性を最大化。
- **CUE自動パース ＆ CUE無しFLACフォールバック**: CUEシート境界を自動パースしてトラック単位に展開。通常 FLAC ファイルへの自動安全フォールバックに対応。
- **VorbisComment 複数値タグの JSONB リスト保存**: `ARTIST` 等のマルチバリュータグを配列 (`["...", "..."]`) として PostgreSQL `meta` (JSONB) カラムへ保持。
- **タイムスタンプ保護（Timestamp Preservation）**: タグ書き戻し時にファイルの作成・更新日時を取得し寸分違わず完全復元。

### 📚 ドキュメント一覧
| ドキュメント | 内容 |
|:---|:---|
| [状態遷移図](docs/state_diagram.md) | パイプライン全体の状態遷移フロー |
| [ER図・データ構造](docs/database_er_diagram.md) | PostgreSQL/SQLite テーブル定義・JSONB仕様 |
| [SHM/WORMアーキテクチャ](docs/shm_architecture.md) | 共有メモリ管理・ゼロコピーIPC |
| [CPU並列処理・RAM制御](docs/cpu_parallelism_and_ram_guard.md) | ワーカー並列化・Gatekeeper・RAM制御 |
| [CUEパースフロー](docs/cue_parsing_flow.md) | CUEシート検出・フォールバック判定 |
| [DLQ・エラーリカバリ](docs/dlq_error_recovery.md) | Dead Letter Queue・ゾンビタスクリセット |
| [GPU/RAMフォールバック](docs/gpu_fallback_and_ram_defense.md) | CUDA/DirectML/Blackwell対応・VRAM解放 |
| [Blackwell RTX 50xx インストール](docs/install_blackwell_rtx50.md) | NVIDIA RTX 50xx シリーズ (CUDA 13.2) セットアップ |
| [治具スクリプト集](docs/utility_tools.md) | 独立治具集 (zig/) の仕様・使用例 |

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
> 使用する外部モデル（Essentia の ONNX 分類器モデル等）のライセンス条項（AGPLv3 / CC 等）を必ずご確認ください。
