# Flac_Analyzer
### 💎 圏論的トポロジーに即した極上の音響解析 ＆ 高貴なる Mood Tagger 💎

**Flac_Analyzer** は、FLACファイル（特にCUEシート埋め込みタイプ）から音響特徴量および音楽的 Mood などを高精度に抽出し、FLACメタデータへの安全なアトミック書き込み（タイムスタンプ完全継承）と PostgreSQL データベースへの永続化（JSONB形式 ＆ CoMonad的履歴自動退避）を同時に成し遂げる、極めて堅牢で美しい音響解析パイプラインですわ！おーほほほほ！

---



## 🏛️ 圏論的モジュール構成 (Architecture)

本スクリプトは、モジュール間の依存関係を射（Morphism）として厳密に定義し、不要な関心の混在を徹底的に排除した美しいトポロジー構成を採用しておりますの！

```mermaid
graph TD
    constants[constants.py <br> 極小対象定義空間] --> models[models.py <br> 余代数的リソース状態空間]
    constants --> analyzer[analyzer.py <br> 自己随伴的 Applicative ドメイン層]
    models --> pipeline[pipeline.py <br> 自然変換合成空間]
    analyzer --> pipeline
    db[db.py <br> IO Monad 境界空間] --> pipeline
    pipeline --> main[main.py <br> 自明な開始射 / Entrypoint]
```

*   **`constants.py` (極小対象定義空間)**:
    *   他の如何なるモジュールにも依存しない、純粋データ定義対象（和音辞書、ノート名リスト等）の配置。
*   **`models.py` (余代数的リソース状態空間 / Comonad)**:
    *   `GLOBAL_ONNX_SESSIONS` / `GLOBAL_DEMUCS` というハードウェア資源（ONNX/分離モデル）の Cofree 構造の管理と、直列実行による推論射の定義。
*   **`analyzer.py` (自己随伴的 Applicative ドメイン層)**:
    *   遅延キャッシュ (CSE) 内包対象 `AudioContext`、特徴量抽出を合成する Applicative 関手 `FeatureExtractor`、およびコドメイン対象 `LibrosaFeatures`/`EssentiaFeatures` のドメイン集約。
*   **`db.py` (IO Monad 境界空間)**:
    *   PostgreSQL への接続・UPSERT副作用を末端に押し出す境界作用の隔離。
*   **`pipeline.py` (自然変換合成空間)**:
    *   音声デコード、Cuesheetパース、セグメンテーション、および解析・DB挿入・タグ書込フローの合成。`psutil` によるシステム資源動的検知（`get_segment_workers`）もここにカプセル化（Lazy Evaluation）されています。
*   **`main.py` (自明な開始射)**:
    *   極薄のエントリーポイント。ディレクトリ走査と `pipeline` への起動命令のみを記述。

---

## 🚀 使い方 (Usage)

### 1. 解析モデルの準備
`models/` ディレクトリを作成し、必要な ONNX 推論モデル（Essentia分類器など）およびクラス定義（JSON）を配置してください。
> [!NOTE]
> `discogs-effnet-bs64-1.onnx` や各分類器モデル (`genre_rosamerica-discogs-effnet-1.onnx` 等)、および対応するクラス定義 JSON ファイルが必要です。

### 2. 動作環境の構築
Python 3.12 または 3.13 の仮想環境（venv）を構築し、パッケージをインストールいたしますわ。
> [!WARNING]
> Python 3.14 では動作確認が取れておりません。3.13 以下の環境をご用意ください。

```powershell
# 仮想環境の作成と有効化
py -3.13 -m venv .venv
. .\.venv\Scripts\Activate.ps1

# pipのアップグレードと依存パッケージの導入
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

### 3. データベースの設定 (任意)
PostgreSQL データベースへの自動インサートを行いたい場合は、環境変数 `INGESTER_DATABASE_URL` または `DATABASE_URL` に接続URIを設定してください。
（設定されていない場合は、標準出力へダミーの SQL INSERT クエリが出力されますわ）

```powershell
$env:INGESTER_DATABASE_URL = "postgres://username:password@hostname:port/dbname"
```
※事前に `sql/schema.sql` (および必要に応じて `sql/migration_v2.sql`) を PostgreSQL 内で実行して、テーブルを初期化しておいてくださいませ。

### 4. 解析パイプラインの起動
準備が整いましたら、対象のディレクトリを指定して実行するだけですわ！おーほほほほ！

```powershell
python.exe main.py <探査したいFLACディレクトリ>
```

### 5. 【次世代】Go Orchestrator ＆ OOM監視テスト
現在、Peak RAM 効率とプロセスの堅牢性を極限まで高めるため、Pythonから **Go 言語ベースのオーケストレーター (`orchestrator/orchestrator.exe`)** への移行を進めておりますの。
結合テストや OOM (Out Of Memory) 監視、実行時間の精密な計測を行うには、以下の専用スクリプトを実行してくださいませ。DB（PostgreSQL）への書き込みは自動的に回避（`--no-db`）され、ローカルへJSON出力されますわ。

```powershell
python.exe test_integration.py
```

## 🌹 高貴なる主要機能 (Features)

### 1. 音響特徴量の圏論的 Applicative 抽出 (Librosa ドメイン層)
*   **遅延キャッシュ機構（共通部分式除去: CSE）**
    *   `AudioContext` 内に遅延評価プロパティを配備。多重スレッドによる並列解析時でも、重い DSP 計算（STFT, Mel-Spectrogram, Chroma, ビートトラッキング等）の重複計算を完全に根絶し、CPUボトルネックを解消いたしましたわ！
*   **多角的な音響特徴量（16種類以上）の自動算出**
    *   BPM、RMS (Mean/Peak)、Energy (波形平方根平均)、Spectral Centroid (平均/標準偏差)、Spectral Bandwidth、Spectral Flatness、Spectral Rolloff、Zero Crossing Rate、Contrast (7つのサブバンド)、MFCC (8バンド)、および 0-1 スケールの相対 SNR を贅沢に算出。
*   **HNR (調波対雑音比) の厳密なる NAP 評価**
    *   正規化自己相関ピーク (Normalized Autocorrelation Peak: NAP) に基づき、0.0〜1.0 の間で高精度に調波の純度を定量化いたします。

### 2. ONNX推論による音楽的 Mood の直列解析 (Essentia 予測層)
*   **純ONNX特化設計**
    *   重厚で不安定な PyTorch 依存を排除し、推論エンジンを `onnxruntime` に完全統一。Windows環境の GPU 加速（NVIDIA CUDA / AMD DirectML）に動的フォールバック対応しておりますの。
*   **スレッドセーフな直列推論**
    *   並列推論時のセグメンテーションフォルト（Segmentation Fault）を防ぐため、`ONNX_LOCK` による排他制御と `SessionOptions` スレッド制限（`intra_op=1`）により、メモリ安全で優雅な直列推論フローを徹底いたしましたわ。
*   **多面的な音楽性分類**
    *   `danceability`, `genre_dortmund`, `genre_rosamerica`, `genre_tzanetakis`, `mood_acoustic`, `mood_aggressive`, `mood_electronic`, `mood_happy`, `mood_party`, `mood_relaxed`, `mood_sad`, `moods_mirex`, `tonal_atonal` などの多角的な推論値（確率）を 1000倍 にスケーリングしてFLACタグに書き込みます。

### 3. 前段 GLOBAL_DEMUCS による波形分離と Stem 解析 (予測分離層)
*   **将来の HTDemucs (ONNX) 統合を予見したインターフェース設計**
    *   オリジナルである `mix` に加え、`drums`, `bass`, `other`, `vocals`, `guitar`, `piano` の計6つの分離ステムの `AudioContext` を格納する `StemContext` を定義。ステム単位での Librosa 解析およびエネルギー比に基づく 0-1 相対 SNR を算出し、ボーカルやベースの強度を的確に把握しますわ。

### 4. Cuesheet のマルチパーシング ＆ 柔軟なフォールバック
*   **三系統 of Cuesheet 境界検出**
    *   Vorbis comment 内の `CUESHEET` タグ、FLACメタデータブロック内の CueSheet 情報、さらには個別の `cue_trackXX_` / `track_XX_` タグから境界（インデックス）を自動検出。
*   **高精度なトラック情報のマージ・フォールバック**
    *   アルバムアーティスト、タイトル、トラック番号、コンポーザー（Composer）を複数の階層（`albumartist` ➔ `artist` ➔ `Unknown` 等）から高精度に補完・マージし、抜けのない解析を保障しますの。

### 5. アトミックタグ書き込み ＆ タイムスタンプ完全継承 (`Timestamp Inheritance`)
*   **安全なアトミック更新**
    *   一時ファイルにメタデータを書き出し、正常書き換えを確認した後にリプレース（`os.replace`）を行うことで、書き込み中の電源切断や強制終了によるFLACファイルの破損を防ぎます。
*   **タイムスタンプの完全継承**
    *   Windows環境では `ctypes` による Win32 API（`CreateFileW` + `SetFileTime`）を直接召喚、Unix/Linux環境では `os.utime` を駆使し、ファイルの作成日時（`ctime`）、アクセス日時（`atime`）、更新日時（`mtime`）を完璧に元の状態へ復元いたしますわ！

### 6. PostgreSQL JSONB によるデータ永続化 ＆ CoMonad的履歴管理
*   **JSONB カプセル化による DDL 変更不要設計**
    *   音響特徴量（`features`）や分類結果（`predictions`）は生 float 値のまま JSONB 形式にまとめて PostgreSQL へ UPSERT。将来特徴量が増減してもテーブル定義の変更（`ALTER TABLE`）は不要ですわ。
*   **CoMonad的履歴自動退避**
    *   同一音源の `audio_hash`（デコード後波形のMD5）を用いた重複排除に加え、メタデータや特徴量の差分更新を検知した際、PostgreSQLのトリガー関数が旧レコードを自動的に `raw.library_flac_history` へ退避する堅牢なデータ構造を誇ります。
*   **検索性能を高める平坦化カラムとインデックス (Schema v2)**
    *   検索頻度の極めて高い `album_artist`, `album`, `artist`, `title` を専用カラムとして平坦化。B-Treeインデックスおよび JSONB に対する GIN インデックスを適切に設計し、数十万件規模のライブラリでも瞬時にクエリ可能ですわ。

---

## 🌊 解析パイプラインフロー (Pipeline Flow)

音源ファイルの読み込みからメタデータの蒐集、ステム分離、特徴量解析、そしてデータベースへの統合（フォールバック付き）に至るパイプラインの全体フローは、以下の通りでございますわ！

### プログラムの状態図 (State Diagram)

```mermaid
stateDiagram-v2
    [*] --> Init
    Init --> ReadFLAC: mutagenで読み込み
    ReadFLAC --> ExtractTags: メタデータ抽出
    
    ExtractTags --> MultiTrack: CUESHEETあり
    ExtractTags --> SingleTrack: CUESHEETなし
    
    MultiTrack --> Mix
    SingleTrack --> Mix: 波形分離
    
    Mix --> Bass
    Mix --> Drums
    Mix --> Vocals
    Mix --> Other
    
    Bass --> FeatureExtraction
    Drums --> FeatureExtraction
    Vocals --> FeatureExtraction
    Other --> FeatureExtraction
    Mix --> FeatureExtraction
    
    FeatureExtraction --> EssentiaPredictions: mixのみONNX推論
    EssentiaPredictions --> DatabaseUPSERT
    FeatureExtraction --> DatabaseUPSERT: mix以外
    
    DatabaseUPSERT --> RealDB: 接続可能
    DatabaseUPSERT --> DummySQL: 接続不可
    RealDB --> WriteTags: FLACタグアトミック更新
    DummySQL --> WriteTags: FLACタグアトミック更新
    
    WriteTags --> [*]: 終了
```

### 処理のシーケンス図 (Sequence Diagram)

```mermaid
sequenceDiagram
    participant Main as main.py
    participant Pipeline as pipeline.py
    participant Models as models.py
    participant Analyzer as analyzer.py
    participant DB as db.py
    
    Main->>Pipeline: FLACファイル解析開始
    activate Pipeline
    Pipeline->>Pipeline: メタデータ&Cuesheet抽出
    Pipeline->>Models: GLOBAL_DEMUCSによる波形分離
    activate Models
    Models-->>Pipeline: 各Stem波形 (mix, drums, bass, etc.)
    deactivate Models
    
    par Stemごとの特徴量抽出
        Pipeline->>Analyzer: Stem波形の解析要求
        activate Analyzer
        Analyzer-->>Pipeline: Librosa音響特徴量 (features)
        deactivate Analyzer
    end
    
    Pipeline->>Models: ONNXモデルによるMood/Genre分類
    activate Models
    Models-->>Pipeline: Essentia予測結果 (predictions)
    deactivate Models
    
    Pipeline->>DB: UPSERT (meta, features, predictions)
    activate DB
    DB-->>Pipeline: 保存完了
    deactivate DB
    
    Pipeline->>Pipeline: FLACファイルへのタグ書き戻し
    Pipeline-->>Main: 解析完了
    deactivate Pipeline
```

---

## 🗄️ データベース設計と永続化 (Database Architecture)

PostgreSQL における `JSONB` のカプセル化と、トリガーを用いた履歴の自動退避（CoMonad的振る舞い）によって、極めて堅牢かつ柔軟なデータ永続化を実現しておりますの。

### 実体関連モデル (ER Diagram)

```mermaid
erDiagram
    raw_library_flac ||--o{ raw_library_flac_history : history
    
    raw_library_flac {
        int id PK
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
    }

    raw_library_flac_history {
        int history_id PK
        int library_id FK
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
        timestamp archived_at
    }
```

### JSONB カラム構造の凡例

データベースの `JSONB` カラムには、それぞれ以下のような構造でデータが動的に格納されますわ！

```json
// 【meta】: メタデータ (可変な文字列やリスト)
{
  "album": "Some Album Name",
  "artist": "Some Artist",
  "title": "Some Title",
  "date": "2023-10-27",
  "tracknumber": "01",
  "genre": ["Electronic", "Ambient"]
}

// 【features】: Librosa 音響特徴量 (Stemごとに分離)
{
  "mix": {
    "bpm": 128.0,
    "rms_mean": 0.153,
    "spectral_centroid_mean": 2500.5,
    "zcr_mean": 0.052,
    "chroma_mean": [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.1, 0.2, 0.3]
  },
  "bass": {
    "rms_mean": 0.081,
    "spectral_centroid_mean": 450.2
    // ... mixと同様の音響特徴量
  }
}

// 【predictions】: Essentia 分類結果 (0.0〜1.0 の推論確率)
{
  "danceability": 0.852,
  "genre_dortmund": {
    "electronic": 0.950,
    "rock": 0.050
  },
  "mood_happy": 0.720,
  "mood_sad": 0.105
}
```

---
