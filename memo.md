# Evolution Log

### 2026-06-12 18:47:18
旦那様より `analyze.py` のリファクタリング要望を受託いたしましたわ！
主なリファクタリング方向性：
- **Essentia解析の並列セグフォ対策**: 手続き型（直列）として分離。
- **Librosa解析の圏論的抽象化**: Applicative と Product による Class への合成。
- **データ分離**: FLACタグ用（丸め千倍）と Postgres 挿入用（生 float）を分離保持。
- **テンソル分配並列処理**: 今後、波形分離した numpy Tensor を分配並列処理できる設計にする。
ファイル構成を調査し、`decisions.md`、`method.md`、`knowledge.md`、`issues.md` を作成して計画立案フェーズへ移行しますわ。おーっほっほっほ！

### 2026-06-12 18:56:01
旦那様より追加のご要望がありましたわ！
- **前段**: HTDemucs6S による波形分離（drums, bass, other, vocals, guitar, piano）のプレースホルダーと並列処理への接続を整備。
- **後段**: Postgres への INSERT ダミー処理（標準出力への SQL 発行）を組み込む。
これらの要件を `decisions.md`、`method.md`、`issues.md` および `implementation_plan.md` に統合し、計画をアップデートいたしましたわ！

### 2026-06-12 19:10:20
旦那様より「承認！」の御言葉をいただき、実装・検証フェーズを完了いたしましたわ！
- `analyze.py` を全面的にリファクタリングし、ONNX同期直列化、Applicative/Product Librosa抽出、生データ/FLACタグ分離、HTDemucs6S波形分離プレースホルダー、およびPostgresダミーSQL出力の全てを美しく統合。
- 仮想環境内の Python にてテスト実行し、CUE分割ファイルに対するダミー分離テンソルの並列分配と、各ソースごとの生 float の SQL INSERT文出力、およびFLACタグの従来互換書き込みが完璧に動作することを確認いたしましたわ！
- `task.md` と `issues.md` を完了 `[x]` に更新し、[walkthrough.md](file:///C:/Users/letwir/.gemini/antigravity/brain/dbfce9c3-063a-4827-b330-11333d9b33cc/walkthrough.md) を作成いたしましたわ！おーっほっほっほ！

### 2026-06-12 19:31:10
旦那様より、並列解析のボトルネック分析と設計相談をいただきましたわ！
- 曲の並列解析はロック競合とスレッド数爆発の観点から「悪手」と見なし、Librosaの内部解析を最大最適化する方向へシフトします。
- `AudioContext` に `@property` による遅延キャッシュ機構を設け、STFT/Mel/Chroma の重複計算を1回に削減する共通部分式除去 (CSE) を実施します。
- `StemContext` の分離導入と、`GLOBAL_DEMUCS` による起動時1回のみのモデルロード化。
- Postgres へのデータ流し込み形式を JSONB（辞書）化し、将来のDDL変更を完全排除。
これらを反映した implementation_plan.md (v3) を作成いたしますわ！おーっほっほっほ！

### 2026-06-13 11:32:54
旦那様より、最適化版の動作確認と、今後の開発方針について共有いただきましたわ！
- 変更したFLACタグの書き込み処理は問題なく動作（再開確認完了）。
- 今後の開発マイルストーンとして以下の方針が確定：
  1. Pre-processing: HTDemucsSeparator の実機モデルロード置き換えと正しい SNR 確定（特定のステム波形からのSNRをタグ指定）。
  2. Postgres のテーブル設計と DDL 初期化の整備。
  3. Postgres への JSONB データの送信および接続・挿入テスト。
これらを `decisions.md`、`issues.md` に将来タスクとして追記し、今回のリファクタリングタスクをすべて正常にクローズいたしましたわ！おーっほっほっほ！

### 2026-06-13 13:01:32
旦那様より、波形分離されたステムを用いた真に音楽的価値のある SNR（またはSDR）算出の机上相談をいただきましたわ！
- 従来の簡易的なプレエンファシスによる SNR を廃止し、波形分離した他ステムの総和をノイズと見なす「アプローチ A (ステム相対 SNR / Dominance)」を採用することを決定。
- ターゲットステム $y_{\text{target}}$ に対し、他ステムの総和 $y_{\text{other}}$ を計算し、音圧比率をデシベル単位（$10\log_{10}$ 係数）で算出する。
- 共通部分式除去 (CSE) や並列 Applicative / Product の流れを壊さず美しく統合するため、波形分離直後 (`StemContext` 生成時) に一括計算し、各 `AudioContext` のプライベート変数 `_snr_val` にプレインジェクションするアーキテクチャ設計を合意。
- 分離歪みをノイズとする「アプローチ B (SDR)」と比較検討し、音楽分析およびメタデータ管理における実用性からアプローチ A の優位性を確認。
- 本設計方針を `decisions.md` および `method.md` に反映いたしましたわ！おーっほっほっほ！

### 2026-06-13 13:04:39
旦那様より、mix の SNR 仕様維持とステム個別 SNR の 0-1 float 埋め込み、および HNR 共通適用方針の共有をいただき、さらに「後処理での上書き (Post-Bind Overwrite)」の追加アイデアをいただきましたわ！
- **mix の SNR**: 従来の Librosa プレエンファシス簡易指標をそのまま維持。
- **ステム個別 SNR**: 他ステム総和に対するパワー割合等をベースに $[0.0, 1.0]$ の範囲の float で表現。
- **HNR**: 圏論的（並列 Applicative）な型・構造の破綻を避けるため、全ソース（mix・各ステム）共通で `LIBROSA_HNR` (NAP) の抽出をそのまま維持。
- **後処理オーバーライト (Post-Bind Overwrite) の採択**:
  - `FeatureExtractor` および `AudioContext` 側には依存性を追加せず、並列 Product 合成が完了した後の「同期後処理フェーズ」にて、すでに算出された `energy` 特徴量を再利用して 0-1 SNR を超高速（$O(1)$）に算出する。
  - 算出した値を `LibrosaFeatures.snr` 属性に直接「上書き」し、その後 FLAC タグの書き戻しと Postgres 送信バッファ（JSONB）の格納を行う。
  - パイプラインを汚さず、余計な再計算も一切発生しない「最も美しい」設計として正式に合意。
- 本設計方針を [method.md](file:///a:/Users/letwir/repo/flac_analyzer/method.md) に詳細追記いたしましたわ！おーっほっほっほ！

### 2026-06-13 14:27:00
旦那様より、PostgreSQL のテーブル設計および `raw.library_flac` における JSONB のデータ構造、ならびに Embedded CUE での同曲ハッシュ重複問題に対する相談をいただきましたわ！
- **同曲判定キーの確立**:
  - タグ書き換えで変化しない「音源そのもののハッシュ（デコード後波形配列の MD5）」を `audio_hash` とし、これを一意キーと決定。
  - CUEシートから切り出された曲ごとのセグメント波形 (`seg_audio`) に対しても、numpy 配列のバイト列 (`tobytes()`) から MD5 を算出することで、同一ファイル内の異なる曲に個別の `audio_hash` が生成され、Embedded CUE 問題を完全に克服！
- **コモナド恒等元 (CoMonad Identity) としてのハッシュ取得**:
  - `FeatureExtractor` (Reader Applicative) は DSP 音響解析に専念させ、ハッシュ計算は `AudioContext` の `@property` に委ねる。
  - パイプライン `analyze_segment_pipeline` が `mix_hash` を返すように変更し、関心の分離（関手の純粋性）を完璧に維持。
- **1曲1行の UPSERT ＋ 履歴退避トリガー構成 (Giga-JSONB)**:
  - メインの `raw.library_flac` テーブルは常に「最新の1曲1行」に保ち、インデックスによる8万曲規模の高速スキャンとクエリ記述の容易さを確保。
  - `meta` や `features` が更新された際は、自動的に `raw.library_flac_history` へ退避するデータベース側のトリガー `trg_archive_library_flac` を設計し、`schema_init.sql` を整備。
- テストスクリプトを実行し、`Track 1` の Unified な `INSERT INTO raw.library_flac ...` 文が完璧に StdOut へ出力されることを実機確認いたしましたわ！おーっほっほっほ！

### 2026-06-13 14:54:00
旦那様より本番 DB (db.tigris-tailor.ts.net) の接続情報、およびログイン用ユーザー `ingester:etl` の作成完了をいただき、実機へのインサート・クエリテストを実施いたしましたわ！
- **接続パッケージ導入**: `psycopg2-binary` を導入し、`requirements.txt` に追記。
- **実インサートルーティング実装**: 環境変数（`INGESTER_DATABASE_URL` または `DATABASE_URL`）がある場合に、自動的に `insert_to_postgres_actual` に分岐して実DBへの UPSERT を実行するように `analyze.py` を換装。
- **ログイン＆インサート大成功**: 
  - `ingester:etl` ユーザーでのログイン、および `etl_flac` ロールの自動権限継承（スキーマ raw、テーブル library_flac への書き込み）が実機で完璧に成功！
  - `testFLAC\藍月なくる_ILLUAMOR.flac` の 6 トラック分の音楽特徴量 (JSONB) のインサート完了を確認いたしましたわ。
- **クエリ確認大成功**: 
  - `test_query.py` を作成・実行し、インサートされた最新 of Track 6 の音響特徴量（BPM: 123.05, vocals.snr: 0.126 等）をクエリし、メタデータや各種解析確率が完全に取得できていることを実証いたしましたわ！おーほほほほ！

### 2026-06-13 16:58:38
旦那様より SQL フォルダの内容を v2 基準で統合・整理するご要望をいただきましたわ！
- `sql/schema_init.sql` を最新の v2 スキーマ定義を満たした `sql/schema.sql` に一本化（統合リネーム）します。
- `sql/migration_v2.sql` のトリガー関数内での INSERT カラム順を、最新のテーブル定義のカラム順（`album_artist, album, artist, title` が `meta, features, predictions` より前）に統一します。
- `db.py` 内の PostgreSQL UPSERT クエリのカラム指定およびバインド値順序を、テーブル定義のカラム順に統一して一貫性を確保します。
- `README.md` に記載されている `schema_init.sql` への参照を `schema.sql` に更新します。
- 不要になった `sql/schema_init.sql` を削除します。
これらを反映した `implementation_plan.md` を作成して旦那様のご承認を仰ぎますわ！

### 2026-06-17 02:35:00
旦那様より実機テスト中のエラーログを共有いただき、デバッグおよび修復を実施いたしましたわ！
- **`TemporalSeqFeatures` の修復**: 前回の置換で欠損していた `centroid_mean` 等のデータクラスプロパティ群を `analyzer.py` の `TemporalSeqFeatures` クラスに完全復元いたしましたわ。
- **`UserWarning` の原因解明と downmix ガード**: 
  - `UserWarning: n_fft=512 is too large for input signal of length=2` という警告は、`pipeline.py` から `models.extract_mel_patches` にステレオ波形 `(N, 2)` がそのまま渡された結果、`librosa` が `(samples, channels)` を `(channels, samples)` と誤認して `samples = 2` と判定したため発生しておりましたの。
  - `models.py` の `extract_mel_patches` の冒頭に、入力波形がステレオの場合に自動でモノラルに downmix するガード処理を組み込み、問題を根底から粉砕いたしましたわ！
- 現在、警告をエラーとして捕捉するデバッグモードを設定した上で、実機テスト (`test.py`) の再検証を実行中ですわ。おーほほほほ！

### 2026-06-17 11:29:00
旦那様より、mutagenで取得した全メタデータをデータベースの `meta` JSONB カラムにマージしたいとのご要望をいただきましたわ！
- **実装方針の確定**:
  - `mutagen` オブジェクトから取得したすべてのタグを `raw_tags` 辞書にダンプ。値のリストについて、要素が1つなら文字列に平坦化、複数ならリストを維持する動的平坦化を適用。
  - マルチトラック（Cuesheet分割）処理時には、現在処理中のトラック番号 `XX` に対応する個別タグ `CUE_TRACK_XX_...` のみを選択的に取り込み、プレフィックスを剥いで `meta` JSONB のキー名（例：`event`, `composer`）にマッピング。他のトラックのタグは除外。
  - シングルトラック（Cuesheetなし）処理時には、トラック個別タグを除外した共通タグのみをマージ。
- **実装と検証の実施**:
  - `pipeline.py` を上記方針に従いリファクタリングし、検証テスト (`test.py`) を走らせて動作を確認しておりますわ！

### 2026-06-17 12:05:00
旦那様より、EssentiaのONNXモデルの自動収集機構において、現在ロードされているDiscogs分類器以外のモデルの有無や、入れ忘れているDiscogsモデルの可能性についてのご質問をいただきましたわ！
- **調査結果の整理**:
  - 現在の収集機構は `discogs-effnet-bs64` バックボーンが出力する埋め込み（128次元）を入力とする下流タスク分類器（EffNet-Discogs）のみを自動ロードします。
  - 入れ忘れているDiscogs推論モデルの候補：
    1. `genre_discogs400-discogs-effnet-1.onnx` （EffNetベースの400スタイル分類器）
    2. `discogs-maest-30s-pw-519l-2.onnx` （MAESTベースの519スタイル分類器。モデルフォルダには存在するが、SKIP指定されており、かつ波形を直接入力とするため現行の推論フローでは動作しない）
- **提案アプローチ**:
  - **① 大きく変えずに動くもの**: `genre_discogs400-discogs-effnet-1` をモデルフォルダに設置する。自動収集・推論・DB格納まで完全にノーコードで動作可能だが、400のタグがFLACに直接書き込まれてしまうため、FLACタグの肥大化・汚染のリスクがある。
  - **② 改修が必要なもの（分岐器の追加）**: 
    - FLACタグへの書き込み処理にフィルタ（確率0.1以上または上位5件のみ）を設ける改修。
    - MAESTモデル等の波形入力モデルのスキップを解除し、入力テンソル形式に応じて推論パスを分岐させる改修。

### 2026-06-17 12:28:00
旦那様より、`genre_discogs400-discogs-effnet-1.onnx` が公式に存在しない点をご指摘いただき、自動自己変換（Transform）とクリーンアップ（pip uninstall）のETLフローを `init_dl_model.py` に組み込むご指示をいただきましたわ！
- **自動変換（Transform）の実装**:
  - `inspect_pb.py` を作成し、`.pb` のプレースホルダー入力ノード `model/Placeholder:0` と、出力ノード `model/dense_1/Sigmoid:0` を特定。
  - `init_dl_model.py` に `transform_pb_to_onnx` 関数を追加。
  - **ETLフロー設計**: 
    1. 一時的に `tensorflow` と `tf2onnx` をインストール。
    2. `tf2onnx.convert` を呼び出して `genre_discogs400-discogs-effnet-1.pb` を `genre_discogs400-discogs-effnet-1.onnx` へと自動変換。
    3. 変換完了後、`tensorflow` と `tf2onnx` を即時アンインストールし、依存関係を綺麗にクリーンアップ。
- **検証テスト**:
  - `test.py` を実行し、自動的に `genre_discogs400` が 19 個目の分類器としてロードされ、タグ `cue_track01_essentia_genre_discogs400` に複数値として集約（上位7件など）されて完璧に書き込まれることを確認いたしましたわ！
- **ログ視認性の改善**:
  - Discogs400などの大量のクラス名がロード時に `INFO` ログとして流れるとログが肥大化・汚染されるため、`models.py` 内の個別分類器の登録ログを `DEBUG` レベルに格下げし、`main.py` からキー一覧のみをスマートに出力するように修正いたしましたわ！
### 2026-06-17 14:02:00
旦那様より、JSONBカラムの特徴量肥大化（絡まり）に対する構造整理の提案、および取得可能なのに行っていない追加のLibrosa特徴量やシーケンス（Seq）データの調査・提案を受託いたしましたわ！
- **現状分析**: 
  - 全ステムに対して共通の `LibrosaFeatures` クラスを一律で適用しているため、各ステム（例: vocals）の JSONB 内部に大量のダミー値（キー推定結果、テンポ、ドラムGroove等）が混入し、構造の不一貫性とデータ肥大化が発生していることを特定。
  - 時系列データ（固定長32の `TemporalSeqFeatures` と、可変長フレーム単位の詳細シーケンス）が同じ `sequences` キーの下に同列に平坦に置かれており、データの重要度や用途に応じた階層化がなされていないことを特定。
- **追加のMIR特徴量提案**:
  - `dict_chord.txt` を用いた「コード進行シーケンス (`chord_sequence`)」の自動推定。
  - `librosa.util.sync` を用いた拍ごとの「ビート同期特徴量（Chroma/RMS）」の抽出（固定次元音楽シーケンスへの圧縮）。
  - `vocals` ステムに対する `librosa.pyin` による「ボーカル基本周波数 (F0)」および音域・ビブラート・発声率等の抽出。
  - `drums` ステムに対する「打撃アタック密度 (drums_onset_density)」の抽出。
  - `spectral_flux` やスペクトル統計量（`skewness`, `kurtosis`）による音色ダイナミクスの強化。
- 上記の内容を `knowledge.md` に追記し、旦那様へ詳細なレポートをお送りいたしますわ！おーほほほほ！

### 2026-06-21 07:54:30
旦那様より Tempogram 統計および ZCR (Zero Crossing Rate) のスカラー・シーケンス（時系列）の双方を取得・保存する正式な実装依頼をいただき、実装および検証を完了いたしましたわ！
- **ZCR (Zero Crossing Rate) の拡張**:
  - `analyzer.py` の `_calc_zcr_features` を定義し、スカラー（平均 `zcr_mean`、標準偏差 `zcr_std`）および 32要素固定長の時系列軌跡（`zcr_seq`）の双方を抽出するよう換装。
  - `RawFeatures` へのバインド、FLACタグ出力（`LIBROSA_ZCR_MEAN`, `LIBROSA_ZCR_STD`, `LIBROSA_ZCR`）、PostgreSQL JSONB シリアライズ（`scalars.zcr_mean`, `scalars.zcr_std`, `sequences.zcr`）を実装。
- **Tempogram 統計の確認**:
  - テンポグラムの全体平均（`mean`）、全体標準偏差（`std`）、時間最大強度の平均（`peak`）、シャノンエントロピー平均（`entropy`）、および支配的テンポ（BPM）の時系列軌跡（`tempo_seq`, 32要素固定長）を抽出する `TempogramFeatures` が正常に機能していることを確認。
  - `scalars` に `tempogram` (mean, std, peak, entropy) を、`sequences` に `tempogram_tempo` (32要素) として完璧に割り当て。
- **検証テスト**:
  - `test_raw_features.py` に Tempogram scalars 内訳および sequences 32要素長の詳細なアサーションを追加し、すべてのテストが完璧にパスすることを確認いたしましたわ！
  - 変更内容をコミットし、リポジトリの状態をクリーンに保ちつつ、既存の直交分離・自然変換パイプラインの圏論的整合性を完璧に維持いたしましたの。お〜ほほほほ！

### 2026-06-21 19:40:00
旦那様より、直列で実行されている Pre-warming 層（DSPの遅延キャッシュ事前評価処理）を CPU コアごとに並列化（マルチプロセス化）可能かどうかのご質問をいただきましたわ！
- **調査と考察**:
  - 現在の直列 Pre-warming は、マルチスレッド環境下での `LIBROSA_LOCK` 競合によるボトルネックを回避するために導入されたもの。
  - プロセス並列化（マルチプロセス）を行えば、各プロセスがメモリ空間・ロックオブジェクトを独立して持つため、`LIBROSA_LOCK` の競合を回避しつつ CPU コアを最大活用することが可能。
  - ただし、Windows 環境における `spawn` 起動コスト、および巨大な NumPy 配列（数十MB）を IPC で受け渡すシリアライズ/デシリアライズ（Pickle）コストが極めて大きい。
  - そのため、安易なマルチプロセス化は通信と起動のオーバーヘッドによって直列よりパフォーマンスが低下（Pessimization）するリスクを孕んでいることを特定。
  - 圏論的な射の適用（Functorial Update）による IPC データの極小化設計案、および不要な Warmup 属性の徹底削減などの代替案を整理。

### 2026-06-21 19:43:00
旦那様より、手元のCPU使用率が「2コアメイン＋4コア少量」程度で動いており、マルチコアを有効に活かしきれていないという現状の動作フィードバックをいただきましたわ！
- **ボトルネックの構造的要因分析**:
  1. 重い「遅延キャッシュの強制評価（STFT, Mel, Beat等）」が直列フェーズにあるため、Python プロセスとしては基本的に1コア分（100%）しか使えない。裏側の BLAS (OpenBLAS/MKL) のスレッド並列により「2コアメイン＋4コア少量」として現れている。
  2. 後段の並列スレッド（ThreadPoolExecutor）フェーズでは、すでにキャッシュが温められているため計算負荷が極小化しており、マルチコアをフルに稼働させる余地がない。
- **改善アプローチの検討**:
  - ミクロな並列化（1ファイル内の Pre-warm のマルチプロセス化）は IPC コストの観点から最適とは言えない可能性が高い。
  - マクロな並列化（バッチスクリプト側でのファイル/セグメント単位のプロセス並列実行）を導入すれば、IPC 通信コストゼロで CPU コアを 100% 限界まで使い切れるため、こちらの方が極めて合理的であると推測。

### 2026-06-21 19:44:00
旦那様より、Pre-warming とその依存後処理（特徴量抽出）をステムなどの「変形した NumPy 行列」ごとにセットにして並列実行するアイデア（-Pre-Warming i- 依存後処理 i）を提示いただき、その構造的検討を行いましたわ！
- **設計的考察**:
  - 各ステムごとに「Pre-warming (STFT/Mel計算等) ＋ 依存特徴量抽出」を1つの実行単位（タスク）としてカプセル化する。
  - **マルチプロセス並列化時のIPC最大問題の解消**:
    子プロセスに渡すのは「対象ステムの波形データ `y`」のみ。子プロセス内で Pre-warming と Librosa 特徴量抽出をすべて実行して自己完結させ、親プロセスへは「最終的な軽量特徴量 (数KB)」のみを返送する。
    これにより、巨大な中間 NumPy 配列（STFT や Mel、Tempogram 等）を IPC で転送するオーバーヘッドが完全に消失する。
  - 親プロセス側では、返ってきた各ステムの特徴量をマージして `DemucsFeatures` 等を組み立てる（Monoidal/Applicative結合）。
  - `LIBROSA_LOCK` もプロセスが分かれるため競合せず、1ファイル解析の中でもマルチコアを極めてクリーンかつ効率的に使い切る素晴らしい設計案であることを特定。

### 2026-06-21 19:45:00
旦那様より、並列化大改造に向けて `analyzer.py` および `pipeline.py` の新しいファイル名（モジュール設計）の検討依頼をいただきましたわ！
- **設計分離案と命名候補の整理**:
  1. 直感・実用系: `analyzer_parallel.py` / `pipeline_parallel.py`
  2. 責務分散（SOC）系: `worker.py` (子プロセス用) ＋ `orchestrator.py` (親プロセス制御用) ＋ `analyzer.py` (ピュア抽出器)
  3. 圏論・代数系: `coproduct_analyzer.py` (余直積ドメイン並列) ＋ `morphism_pipeline.py`
- 上記のモジュール役割分担と命名案を整理し、旦那様へ提案とブレインストーミングを促すレポートをお送りいたしますわ。

### 2026-06-21 19:48:00
旦那様より並列処理のインポート依存関係とフローの認識（`analyzer_func.py` を各ステム用ワーカーからインポートし、集計プロセスの `analyzer_coproduct.py` で集約して後段へ繋ぐ）について確認をいただき、設計の精査を行いましたわ！
- **認識の整合確認**:
  - 旦那様の認識はアーキテクチャの基本フローとして完全に正しいことを確認。
  - **更なる最適化（ファイル数のスリム化）の検討**:
    各ステムごとに別々の物理ファイル（`analyzer_mix.py`, `analyzer_drum.py` 等）を定義すると、Windows の `spawn` 起動時に別々のモジュールインポートオーバーヘッドが発生し、ファイル管理も煩雑になる。
    代替案として、`analyzer_func.py` 内に「引数 `stem_name` で処理を動的に分岐する単一のワーカー関数（`run_stem_worker`）」を定義し、`analyzer_coproduct.py` からその同一の関数に対して引数違いでタスクを並列 dispatch する設計の方が、インポート効率・コード保守性の両面で極めて優れていることを特定。

### 2026-06-21 19:51:00
旦那様より、将来的に `drums` 等に新しい Librosa 検証を追加した際の拡張性・実装難易度についての追加質問をいただき、保守性分析を行いましたわ！
- **アプローチ別難易度評価**:
  1. 物理ファイル分離型: 難易度・低。対象ファイル（`analyzer_drum.py`）のみを書き換えるため安全性が高いが、インポートオーバーヘッドは残る。
  2. 1ワーカー集約型（if-elif分岐）: 難易度・低。ただし単一ファイル内の条件分岐が肥大化し、他ステムへのエンバグ懸念が残る。
- **究極の解決策（OCP: 開放閉鎖原則に基づく宣言的マッピング設計）の提示**:
  - `analyzer_func.py` 内に各ステムの抽出設定（Warmupリストや適用する Extractor 射）を静的辞書 `STEM_CONFIG` として宣言的に定義する。
  - `analyzer_worker.py` はこの設定をメタ的に読み込んで実行するだけの汎用ランナーとする。
    - この設計により、将来の検証追加時は `analyzer_func.py` の設定辞書と関数のみを変更するだけで済み、並列化ランナーコードを一切汚さずに拡張可能（難易度：極小かつ極めて安全）であることを特定。

### 2026-06-21 22:53:00
旦那様より各ステムで centroid および tempogram_tempo 特徴量が抽出できていないとのご指摘を受け、調査・改修を行いましたわ。
- **不具合と原因**:
  - `_calc_temporal_seq` と `_calc_tempogram_features` に mix/drums/bass ステム以外の抽出をスキップ・ダミー化する制限が存在し、他ステムでの抽出が機能していなかった。
  - `tempogram_tempo` の推定時にBPM無限大 (`inf`) が混入して JSON 保存時に null に変換されていた。
- **実施した改修**:
  - `AudioContext` に `centroid` キャッシュプロパティを追加し、重複計算をCSEで根絶。
  - `_calc_temporal_seq` および `_calc_tempogram_features` のステム別制限を撤廃して全ステムで動作するように一般化。
  - テンポグラムの argmax 探索からインデックス 0 (`inf`) を除外するよう修正。
  - `_stem_filter_scalars` & `_stem_filter_sequences` で全ステムに tempogram_tempo と centroid_seq が返るように修正。
  - `STEM_CONFIGS` ですべてのステムの `warmup` に `"tempogram"`, `"onset_env"`, `"tempobeat"` を追加。
- **検証結果**:
  - テストファイルによる検証で、全ステムにおいて centroid_seq（32要素）と tempogram_tempo（32要素）が非 `None`・非ゼロの実データとして完璧にデータベースに保存されることを確認。

### 2026-06-21 23:01:00
旦那様より「取れるデータは全部取る」との追加ご指示を受け、ステム制限フィルターの完全撤廃およびシリアライズのさらなる安定化・堅牢化を行いましたわ。
- **追加の改修**:
  - `_stem_filter_scalars` & `_stem_filter_sequences` からステム別の条件分岐を完全撤廃。全ステムで `chroma`, `mfcc`, `tonnetz`, `key`, `onset`, `groove`, `section` などの全詳細特徴量を一切間引くことなくデータベース（JSONB）へ出力・保存するように拡張。
  - 全ステムの `warmup` 設定を `mix` と同一のフルセット（`stft`, `spectro`, `power`, `chroma`, `mel`, `hnr`, `chroma_cqt`, `tempobeat`, `onset_env`, `tempogram`）に標準化。
  - `db.py` の `SafeAudioJSONEncoder` において numpy 固有の型（np.floating, np.integer, np.ndarray）の Python 標準型への自動キャスト、および nan/inf の安全置換処理を網羅的に強化。
  - `analyzer_worker.py` の Pre-warming ループにおいて、例外発生時にログ警告を残しつつも後続の抽出を安全に続行させる防護 try-except ブロックを実装。
- **検証結果**:
  - データベース実機インサートテストにて、全ステムで全特徴量が一切間引かれることなく完璧に数値実データとして保存されていることを確認。

