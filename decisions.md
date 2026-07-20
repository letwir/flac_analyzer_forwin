# Go+Python 共有メモリアーキテクチャへの移行決定書 (Single Source of Truth)

旦那様の 5950X ＆ 64GB RAM ＆ RTX 3060 環境において、OOMキラーを回避し、DB待機（12分に及ぶUPSERTロック等）によるリソース稼働率の低下を根絶するための、関数型・圏論的アーキテクチャ移行の最終決定事項です。

## 1. 責務の厳格な境界分割 (Actor Model)

- **PowerShell (ps1)**: 
  ディレクトリの再帰走査により対象の FLAC ファイルパスを取得し、Goオーケストレーターのタスクキューへ一方向で投下する。
- **Go (Orchestrator & IO Monad)**: 
  - ファイルパスの内部情報（メタデータ等）には関与せず、単なるタスク識別子として扱う。
  - Windows API (`CreateFileMapping`) を用いて波形引き渡し用の共有メモリ領域を確保し、その「ポインタ名（所有権）」をPythonワーカーへメッセージパッシングとして渡す。
  - `demucs_worker.py` を起動し、完了ステータス (`exit 0`) を確認したのち、共有メモリ領域に対して `Freeze()`（PAGE_READONLY 化）の射を適用する。
  - その後、`librosa_worker.py` を起動し、Freeze済みの共有メモリから並列に特徴量を抽出させる（完全疎結合アーキテクチャ）。
  - DBアクセス（PostgreSQLへの INSERT / UPSERT）を非同期の Goroutine で完全に引き受け、Pythonプロセスのブロッキングを排除する。
- **Python (Worker & Pure Morphism)**:
  - **`demucs_worker.py`**: 引数で渡された共有メモリ名を開き、Demucs分離後のステム波形を書き込んで `exit 0` する。
  - **`librosa_worker.py`**: 渡された共有メモリを Read-Only でアタッチし、Librosa特徴抽出を実行してJSONを返す。プロセスは単一タスク完了と共に破棄され、メモリを全解放する。

## 2. DBスキーマと波形特徴量 (JSONB) の構成

楽曲ごとの分析は厳格に分離され、巨大な配列データはファイルタグではなく PostgreSQL へ JSONB として格納します。

- **`meta` カラム**: EAC抽出ログ、CUESHEET情報、FLAC VorbisComment 等、ソース由来の不変データを格納。
- **`features` カラム**: Demucsで分離された各ステム (`mix`, `bass`, `drums`, `vocals`, `other`) 空間ごとに、以下の2層を配置する。
  - `scalars`: 平均値(mean)、標準偏差(std)、BPMなどのスカラー統計量。
  - `sequences`: ZCR、RMS、Centroid 等の時間発展を 32フレーム に固定長補間した時系列シーケンスデータ。

## 3. FLACタグへの限定的書き込みと「更新日時の継承」

- **限定的タグ追記**: プレーヤーや検索基盤で即座に参照されるべき「一部の重要スカラー特徴量（BPMや主要指標等）」のみを FLAC 本体のタグ (VorbisComment) に追記します。巨大なシーケンス配列はタグには書き込みません。
- **更新日時の継承 (Timestamp Preservation)**: FLACファイルのタグを更新する前後で、元のファイルの更新日時（`LastWriteTime`）を取得・保持しておき、タグ書き込み完了後に `os.utime` (または ps1 側の `[System.IO.File]::SetLastWriteTime`) を用いて元の更新日時に復元（継承）いたします。これにより、無駄なファイルバックアップ差分の発生を防ぎます。

## 4. 検出器の拡張と `analyzed_at` の追従

- 新たな分析モデル（Essentiaの新規分類器や、Demucsの追加モデル等）がパイプラインに増設された場合、既存楽曲に対する再解析および部分的な JSONB マージ（UPSERT）が発生します。
- この変更をシステム全体で追跡可能にするため、データベース側の `analyzed_at` (解析実行日時) カラムを更新し、どの楽曲が最新の検出器群を通過したかを常にトラッキングいたします。

## 5. Go+SQLite による並列タスク状態管理 (Single Process DB Write)

- **状態管理の集約**: タスクの重複実行防止と進行状態（PENDING/RUNNING/COMPLETED/FAILED）の管理を Go 側の SQLite (`orchestrator.db`) に一元化します。
- **書き込みブロッキングの根絶**: SQLiteの WAL（Write-Ahead Logging）モードおよび `synchronous=NORMAL` を適用。書き込みを唯一のGoプロセスに制限し、PythonプロセスのDBブロッキングやロック競合を排除します。

## 6. エラー情報のキャプチャと終了ステータスの伝達

- **エラーキャプチャ**: Pythonワーカーの異常終了時（終了コード非0）、Goのディスパッチャが標準エラー出力を捕捉し、その最後の詳細なトレースバックやエラーメッセージを SQLite に `error_message` として保存します。

## 7. Postgres送信失敗時のローカルDLQ (Dead Letter Queue) フォールバック

- **データの永続性保証**: Postgresへの接続エラー等が発生した場合、`ingester.py` は解析結果（JSONペイロード）を失うことなく、ローカルの独立した SQLite DB (`send_failed.db`) に退避します。
- **非同期復旧**: `retry_ingest.py` を手動またはスケジューラで起動し、DLQ内の未送信レコードを順次 Postgres へ再送し、成功したレコードのみを DLQ から自動削除します。

## 8. デコード波形ハッシュ（MD5）による事前重複判定と解析バイパス

- **無駄なリソース消費の回避**: 解析実行前に `worker_demucs.py --check-hash-only` により波形データのハッシュ（MD5）のみを抽出し、`ingester.py --check-hash` を通して PostgreSQL に問い合わせる。
- **解析のバイパス**: すでにデータベース（PostgreSQL）に該当ハッシュのレコードが存在する場合は、重い音源分離（Demucs）や各種特徴量抽出（Librosa, Essentia等）をすべてスキップし、タスクを即時完了とみなすことで、CPU/GPUおよびVRAMの浪費を100%防止する。

