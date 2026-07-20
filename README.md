# Flac_Analyzer

## 何これ？
FLAC形式の音楽ファイル（CUEシートによるインデックス分割を含む）から音響特徴量（テンポ、音量、周波数成分など）を抽出し、AIモデルによってジャンルやムードを分類・永続化するツールです。

Windows環境でのバッチ処理に特化しており、50GBを超える大量の楽曲ライブラリを処理する際でもメモリ不足（OOM）でクラッシュしないよう、以下の仕組みを組み込んでいます。
- **Go言語による並行ジョブ管理**: ディスパッチャが利用可能なシステム空きメモリやCPU負荷を監視し、ワーカープロセスの同時実行数を制御します。
- **Windows共有メモリ（Shared Memory）WORM転送**: Pythonワーカーでデコード・音源分離した巨大な波形データをメモリ上の共有領域（Write-Once Read-Many）にアタッチさせ、プロセス間での不要なデータコピーや断片化を防止します。
- **PostgreSQL への非同期永続化**: 抽出結果を JSONB フォーマットでDBへUPSERTし、失敗時はローカルの SQLite データベースへ一時退避（DLQ）します。
- **タイムスタンプ保護（Timestamp Preservation）**: 解析結果の一部を FLAC 本体のタグへ安全に書き戻しますが、その前後でファイルの「作成日時」「最終アクセス日時」「更新日時」を取得し、寸分違わず完全に復元します。

---

## 使い方

### 1. 動作環境の要件
- OS: Windows 11 (64bit)
- Python 3.12 または 3.13（仮想環境を推奨）
- Go (開発・ビルド用。事前コンパイル済みの `orchestrator.exe` を直接利用することも可能)
- PostgreSQL (解析結果の保存先)

### 2. 環境構築
Python 仮想環境（venv）を作成し、必要なライブラリをインストールします。

```powershell
# 仮想環境の作成と有効化
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1

# 依存パッケージのインストール
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

#### 💡 GPU (NVIDIA CUDA / DirectML) 加速のためのセットアップ手順
システム上の物理 GPU を活用して音源分離（Demucs）や分類推論（Essentia/ONNX）を高速化したい場合は、環境に合わせて以下の追加設定を行いますわ。

1. **NVIDIA GPU (CUDA) を使用する場合**:
   - `onnxruntime-gpu` をインストールします（通常の `onnxruntime` や `onnxruntime-directml` とは同じ環境に共存できませんの）。
     ```powershell
     pip uninstall onnxruntime onnxruntime-directml
     pip install onnxruntime-gpu
     ```
   - システムに適切なバージョンの **CUDA Toolkit** および **cuDNN** が導入されている必要がありますわ（onnxruntime-gpu の公式対応表をご確認ください）。
   - PyTorch で CUDA を有効化するため、互換性のあるバージョンを明示してインストールします：
     ```powershell
     pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
     ```

2. **AMD / Intel iGPU などの DirectX 12 経由で実行する場合**:
   - `onnxruntime-directml` をインストールしますわ。
     ```powershell
     pip uninstall onnxruntime onnxruntime-gpu
     pip install onnxruntime-directml
     ```

### 3. 解析モデルの配置
`models/` ディレクトリを作成し、推論用の ONNX 分類器モデルおよびクラス定義 JSON を配置します。
- 例: `discogs-effnet-bs64-1.onnx`
- 各分類モデル（`genre_rosamerica-discogs-effnet-1.onnx` 等）
- 対応するラベルマッピング用 JSON ファイル

### 4. データベースと設定ファイルの準備
PostgreSQL に `sql/schema.sql` を実行して、テーブルおよびトリガー関数を作成しておきます。
また、プロジェクトルートの `config.toml` に、PostgreSQL の接続情報および同時実行ワーカー数を指定します。

```toml
[database]
url = "postgres://username:password@hostname:port/dbname"

[orchestrator]
num_workers = 4
demucs_concurrent_limit = 1
shm_allocation_delay_sec = 2
queue_dir = "../queue"
```

### 5. 実行手順
解析の実行は、常駐型のGoオーケストレーターと、ディレクトリ走査用スクリプトの組み合わせで行います。

#### ステップ1: Go オーケストレーターの起動
`orchestrator` ディレクトリでプログラムを起動します。タスク受付用の HTTP サーバー（ポート `8080`）とメトリクス公開用サーバー（ポート `2112`）が立ち上がります。

```powershell
# ビルドして実行する場合
cd orchestrator
go build -o orchestrator.exe
.\orchestrator.exe
```

#### ステップ2: 解析リクエストの送信（ディレクトリ一括走査）
別ウィンドウで PowerShell スクリプトを実行し、解析したいFLACディレクトリを指定します。発見されたファイルパスが自動的にオーケストレーターの API へ転送されます。

```powershell
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library"
```

#### ステップ3: PostgreSQL送信失敗時（DLQ）の再送処理
万が一解析の完了時に PostgreSQL がダウンしていた場合、解析データはローカルの `send_failed.db` (SQLite) に自動退避されます。DB復帰後に以下のスクリプトを呼び出すことで、未送信データを安全に PostgreSQL に再送できます。

```powershell
python.exe retry_ingest.py
```

-------------

## 詳しい内容
本解析システムが備える詳細な特徴と実装の強みは以下の通りです。

- 🧠 **高度な音源分離（Demucs ONNX）**
  - 入力された音源から `mix` / `drums` / `bass` / `vocals` / `other` などの各ステムに自動分離します。ボーカル単体の明瞭度やベース・ドラムの強度を個別に測定可能です。
- 🧱 **メモリ安全性とOOM対策（Freeze SHM）**
  - メモリ枯渇を防止するため、分離後の numpy 配列を Windows の共有メモリハンドル経由で後続プロセスに渡します。Go 側が一時的にメモリ領域を `PAGE_READONLY`（Freeze）化することで、読み取り専用として安全に複数ワーカーが並列アタッチします。
- 🎨 **16種類以上の音響特徴量抽出**
  - Librosa を使用し、BPM、RMS (平均/ピーク値)、ZCR (ゼロ交差率)、Spectral Centroid、Contrast、MFCC (8バンド) などを抽出。時間発展を固定フレームに補間した時系列シーケンスデータも蓄積します。
  - 自己相関ピーク（NAP）による HNR（調波対雑音比）測定を実装し、ハーモニクスの純度を数値化します。
- 🔮 **ONNXランタイムによる多次元 Mood & ジャンル推論**
  - 重厚な PyTorch に依存せず、推論エンジンを `onnxruntime` に統一。
  - `danceability`, `genre_rosamerica`, `mood_happy`, `mood_sad` などの推論値（確率）を 1000倍 にスケーリングして保持します。
- 💾 **PostgreSQL JSONB 永続化とトリガー履歴退避**
  - 予測スコアや時系列データは PostgreSQL の `JSONB` 型へ UPSERT。将来のモデル増設による項目追加時もテーブル定義の変更（`ALTER TABLE`）が不要です。
  - 同一音源（波形MD5）のメタデータや特徴量に変更を検知した際は、DB側トリガーが古いデータを `raw.library_flac_history` へ自動退避し、詳細な編集履歴を残します。
- 📈 **Prometheus を用いたリアルタイム監視**
  - ポート `2112/metrics` にて Prometheus 形式のメトリクスを自動公開します。処理中のキュー件数や実行中のワーカー数、OOM による強制待機の有無などを Grafana 等で可視化可能です。

-------------

## プログラムの状態遷移図
Go オーケストレーターと Python ワーカープロセス群の連携およびタスク処理の流れを示す状態遷移図です。

```mermaid
stateDiagram-v2
    [*] --> Idle
    Idle --> TaskReceived: /task APIへファイルパスがPOSTされる
    TaskReceived --> CheckState: state.db（SQLite）で処理状況を確認
    
    CheckState --> Skipped: 重複あり (COMPLETED / RUNNING)
    CheckState --> Queued: 未処理または過去にFAILED (PENDINGとして登録)
    
    Skipped --> [*]: レスポンス 200 OK (スキップ)
    
    state Dispatcher_Loop {
        Queued --> ResourceWait: 空きRAMおよびDemucs並行上限セマフォを監視
        ResourceWait --> AllocatingSHM: Windows 共有メモリ領域を動的確保
        AllocatingSHM --> DemucsProcessing: python worker_demucs.py 起動（波形デコード・分離・SHMへ書込）
        DemucsProcessing --> FreezingSHM: Go側で共有メモリを PAGE_READONLY 化 (Freeze)
        FreezingSHM --> FeatureExtracting: 各種解析プロセスを並列起動（Librosa / Tensor / Essentia）
        FeatureExtracting --> Ingesting: python ingester.py 起動（解析結果のJSON集約）
    }
    
    Ingesting --> PostgreSQL_Upsert: PostgreSQL 接続可能
    Ingesting --> DLQ_Fallback: PostgreSQL 接続不可 (send_failed.db へ保存)
    
    PostgreSQL_Upsert --> Cleanup: 共有メモリの破棄、一時JSONファイルの削除
    DLQ_Fallback --> Cleanup
    
    Cleanup --> TaskCompleted: state.db の status を COMPLETED (または FAILED) に更新
    TaskCompleted --> [*]
```

## ER図
システムが使用するデータベース（PostgreSQL メメインおよび履歴、Go側管理用の SQLite 2種）の実体関連図です。

```mermaid
erDiagram
    %% PostgreSQL Tables
    raw_library_flac ||--o{ raw_library_flac_history : "BEFORE UPDATE (Trigger)"
    
    raw_library_flac {
        int id PK "主キー(SERIAL)"
        string audio_hash UK "波形デコードデータのMD5(32文字)"
        string filepath "ファイル絶対パス"
        string filename "ファイル名"
        int track_number "トラック番号"
        string album_artist "アルバムアーティスト(平坦化)"
        string album "アルバム名(平坦化)"
        string artist "曲アーティスト(平坦化)"
        string title "曲タイトル(平坦化)"
        jsonb meta "FLACタグ等の生データ"
        jsonb features "Librosa等の抽出特徴量"
        jsonb predictions "Essentiaによる予測スコア"
        timestamp collected_at "レコード収集日時"
        timestamp analyzed_at "解析完了日時"
    }

    raw_library_flac_history {
        int history_id PK "履歴主キー"
        int library_id FK "メインテーブルへの参照"
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
        string status "タスク状態(PENDING/RUNNING/COMPLETED/FAILED)"
        string error_message "例外・クラッシュ詳細"
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
        json meta "退避タグデータ"
        json features "退避特徴量データ"
        json predictions "退避予測データ"
        datetime failed_at "送信失敗日時"
    }
```

## ER図に載せると冗長なjsonB内容
テーブルに格納される `JSONB` カラム（`meta`, `features`, `predictions`）の具体的なデータフォーマット構造のサンプルです。

### 1. `meta` カラム
FLACタグ（VorbisComment）から抽出した全ての生データをそのままディクショナリ構造で記録します。
```json
{
  "album": "Some Album Name",
  "artist": "Some Artist",
  "title": "Some Title",
  "date": "2023-10-27",
  "tracknumber": "01",
  "genre": ["Electronic", "Ambient"],
  "albumartist": "Some Album Artist",
  "cuesheet": "..."
}
```

### 2. `features` カラム
分離されたステム (`mix`, `bass`, `drums`, `vocals`, `other` など) ごとに、スカラー値（scalars）および固定フレーム数に補間された時間発展のシーケンスデータ（sequences）を格納します。
```json
{
  "mix": {
    "scalars": {
      "bpm": 128.0,
      "rms_mean": 0.153,
      "rms_std": 0.045,
      "energy": 45.2,
      "spectral_centroid_mean": 2500.5,
      "spectral_centroid_std": 600.2,
      "spectral_bandwidth_mean": 1800.3,
      "spectral_flatness_mean": 0.012,
      "spectral_rolloff_mean": 5200.0,
      "zcr_mean": 0.052,
      "hnr_nap": 0.825
    },
    "sequences": {
      "rms": [0.08, 0.12, 0.15, 0.18, 0.16, "...(合計32要素)"],
      "spectral_centroid": [1200.0, 1500.0, 2200.0, 2600.0, "..."],
      "mfcc": [
        [-120.0, -115.0, -110.0, "..."],
        [40.0, 42.0, 45.0, "..."],
        ["...(各バンドごとの配列、計8バンド)"]
      ]
    }
  },
  "bass": {
    "scalars": {
      "rms_mean": 0.081,
      "spectral_centroid_mean": 450.2
    }
    // ... mixと同様の項目がステム特性に応じて格納されます
  }
}
```

### 3. `predictions` カラム
Essentiaの分類モデルで推定された確率スコアを格納します（パーセンテージに合わせるため 0.0〜1.0 の値を 1000 倍した整数値で格納します）。
```json
{
  "danceability": 852,
  "tonal_atonal": 910,
  "mood_happy": 720,
  "mood_sad": 105,
  "genre_dortmund": {
    "electronic": 950,
    "rock": 50,
    "pop": 0
  },
  "genre_rosamerica": {
    "house": 800,
    "techno": 150,
    "classical": 50
  }
}
```
