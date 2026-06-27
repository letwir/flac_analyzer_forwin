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

```mermaid
flowchart TD
    Start([FLACファイルの解析開始]) --> ReadFLAC[1. FLACファイルの読み込み & mutagen.FLACの生成]
    ReadFLAC --> ExtractRawTags[2. mutagenから全メタデータを抽出 <br> <b>raw_tags</b> 辞書の構築 <br> ※1要素なら文字列へ平坦化、複数ならリスト維持]
    
    ExtractRawTags --> DetectCuesheet{3. CUESHEETの存在判定}
    
    %% Cuesheetありの分岐
    DetectCuesheet -- Cuesheetあり (マルチトラック) --> LoopTracks[4. 各トラックのループ処理 (num = 1, 2, ...)]
    LoopTracks --> FilterTrackTags[5. raw_tagsから自トラック用メタデータを抽出 <br> <b>CUE_TRACK_num_TAGNAME</b> に合致するもののみ <br> プレフィックスを剥いで <b>track_meta</b> にマージ]
    FilterTrackTags --> ResolveTrackInfo[6. トラック個別解決値 (title, artist, duration 等) で上書き]
    ResolveTrackInfo --> SliceAudio[7. トラック境界のサンプル位置で波形を切り出し]
    SliceAudio --> run_demucs_multi[8. GLOBAL_DEMUCS による波形分離]
    
    %% Cuesheetなしの分岐
    DetectCuesheet -- Cuesheetなし (シングルトラック) --> FilterCommonTags[4'. raw_tagsから個別トラックタグを除外 <br> 共通タグのみを <b>track_meta</b> にマージ]
    FilterCommonTags --> ResolveGlobalInfo[5'. ファイルグローバル情報 (title, artist, duration 等) で上書き]
    ResolveGlobalInfo --> run_demucs_single[6'. GLOBAL_DEMUCS による波形分離]
    
    %% 共通の解析・統合フロー
    run_demucs_multi --> run_librosa[9. 各ステムへの Librosa 特徴量並列抽出 <br> ＆ 相対 SNR 算出]
    run_demucs_single --> run_librosa
    
    run_librosa --> run_essentia[10. mix ステムへの Essentia 直列ONNX推論]
    
    run_essentia --> assemble_sql[11. SQLインサートデータの組み立て]
    assemble_sql --> db_uri_check{12. 接続環境変数 の存在判定}
    
    db_uri_check -- 存在し接続可能 --> actual_insert[13. PostgreSQLへの実インサート <br> <b>raw.library_flac</b> への UPSERT]
    db_uri_check -- 存在しない or 接続失敗 --> dummy_sql[13'. ダミーSQLの標準出力ダンプへのフォールバック <br> ※UTF-8安全出力]
    
    actual_insert --> write_tags[14. 特徴量タグを FLAC ファイルへ書き戻し <br> ※アトミック更新 & タイムスタンプ完全継承]
    dummy_sql --> write_tags
    
    write_tags --> End([解析完了])
```

---
