# History Log

### 2026-07-17 08:14:00

- [x] DONE: config.toml を無効なDBポートに一時変更し、ingester.py を実行した際に正しく DB 接続エラーが発生して DLQ (SQLite: send_failed.db) にペイロードが退避されることを確認。
- [x] DONE: ingester.py 内で発生した UnboundLocalError (ローカルスコープでの二重 import json に起因する json.load の名前空間衝突) を、ローカルインポートを排除することで修正。
- [x] DONE: postgresql-x64-18 に接続してテストデータベース flac_analyzer_test を作成し、sql/schema.sql にてスキーマおよびロール etl_flac / ingester / analyzer を初期化。
- [x] DONE: FLAC_DB_URL を正しく設定した状態で retry_ingest.py を実行し、DLQ から PostgreSQL 側 raw.library_flac テーブルへ UPSERT され、SQLite (failed_payloads) からデータが削除されたことを実機検証。


### 2026-06-22 16:32:00

- [x] DONE: ユーザーが `feature` スキーマを削除されたことに伴い、`raw.library_flac` の DELETE 実行時の `psycopg2.errors.ForeignKeyViolation` エラーが自然解消したことを確認。
- [x] DONE: `load_wave.py` の `save_stems` から `clear_producer_shm_cache()` 呼び出しを削除し、SharedMemory ハンドルの早期解放バグ（`WinError 2`）を解消。
- [x] DONE: `pipeline.py` の `run_producer` の末尾に、Consumer の Queue 処理完了（`completed.value == enqueued.value`）まで待機するループおよび `load_wave.clear_producer_shm_cache()` 呼び出しを追加し、正しい生存期間でのリソースのセーフクリーンアップを実現。
- [x] DONE: `main.py` の進捗監視ループに、デッドロック防止の安全弁（全ての Consumer プロセスが終了しているが Producer が生存している場合に producer.terminate() を実行）を実装。
- [x] DONE: `pytest` による自動テストを実行し、テストがすべて正常にパスすることを確認。

### 2026-06-22 15:47:50

- [x] DONE: `load_wave.py` の SharedMemory オブジェクト生存管理の実装 (モジュールレベルキャッシュ `_SHM_KEEP_ALIVE` および `clear_producer_shm_cache()` による Windows での SharedMemory 即時破棄問題の完全解決)
- [x] DONE: `flac_decode.py` の `build_flac_handle` 内における `filepath` の絶対パス化修正 (テスト時のパス比較不一致によるアサーションエラーの解決)
- [x] DONE: `flac_decode.py` の `parse_wav_header` 内での WAVEFORMATEXTENSIBLE (0xFFFE) オフセット配置パースの修正 (cbSize および GUID 読み出しのズレの修復により、24bit/32bit FLAC デコード時の `wFormatTag == 0` 不具合を解消)
- [x] DONE: 自動テスト `pytest tests/test_flac_decode.py tests/test_load_wave.py -v` の全6項目パス確認

### 2026-06-22 12:35:54

- [x] TODO: リファクタリング実施計画の作成と旦那様のご承認取得
- [x] TODO: Essentia ONNX 解析の手続き型分離（シングルスレッド・直列化）
- [x] TODO: Librosa 解析の Applicative / Product 圏論的抽象化の実装
- [x] TODO: 特徴量データクラス (`LibrosaFeatures` / `EssentiaFeatures`) の実装と FLAC タグ（丸め千倍等）/ 生データ (float) の分離
- [x] TODO: 前段：HTDemucs6S波形分離プレースホルダーと並列パイプラインの実装
- [x] TODO: 後段：Postgres INSERTダミー（標準出力ダンプ）の実装
- [x] TODO: 動作検証（単体テストおよび FLAC ファイルによる実行確認）
- [x] TODO: 新設計による最適化：AudioContext への遅延プロパティキャッシュ (CSE) の実装
- [x] TODO: 新設計による最適化：StemContext と GLOBAL_DEMUCS 方式の導入
- [x] TODO: 新設計による最適化：Postgres への INSERT を JSONB 形式に更新
- [x] TODO: 最適化バージョンの動作検証
- [x] TODO: 前段: HTDemucsSeparator の実機モデルロードへの置き換えと、正しい SNR の確定（※0-1 スケール相対 SNR 算出と上書きロジックの実装完了）
- [x] TODO: 特徴量: Librosa 音楽特徴量の強化 (Chroma 12D, HPSS, Flux, Onset, Tempogram, Dynamic Range, MFCC20) [DONE]
- [x] TODO: データベース: Postgres のテーブル設計と初期化 (DDL) (※schema_init.sql整備完了)
- [x] TODO: データベース: Postgres への JSONB 接続・送信テスト (※ingester:etlによる実機ログイン・INSERTおよびSELECT検証成功)
- [x] TODO: Cuesheet 複数格納場所パースの堅牢化と Postgres JSONB 保管 (※新生テーブル設計の適用・実インサート検証成功)
- [x] TODO: SQLフォルダの内容をv2基準で統合・整理 (※統合・整理・検証完了)
- [x] TODO: 時系列特徴量算出エラーおよびステレオ入力によるn_fft警告バグの修復 (※TemporalSeqFeaturesクラス修復およびextract_mel_patchesのdownmixガード追加により解決)
- [x] DONE: mutagen全メタデータのmeta JSONBマージと個別トラックフィルタリング
- [x] DONE: Demucsステム (drums, bass) への tempobeat 抽出拡張と Pre-warming 整合
- [x] DONE: Tempogram 統計および ZCR (スカラー ＆ シーケンス双方) の抽出実装とテスト検証
- [x] DONE: 全ステムでの centroid 軌跡および tempogram_tempo 抽出拡張と BPM inf 不具合の修正
- [x] DONE: 全ステムでの全特徴量（Chroma/MFCC/Key/Onset/Groove等含む）抽出の完全解禁、および db.py / worker.py の堅牢化

### 2026-06-22 12:47:57
<details>
<summary>Method details and specifications</summary>

<methods>
  <target id="ESSENTIA解析の手続き型分離とセグフォ回避">
    ONNX Runtimeの複数スレッドからの同時アクセス、またはOpenMPスレッドとPythonスレッドの競合によるSegmentation Faultを防ぐため、以下の設計を採用する。
    - ONNXセッションのプール（`OnnxSessionPool`）を廃止し、セッションはグローバルで各モデルにつき1つだけ保持する。
    - `EssentiaAnalyzer` クラスまたはモジュールを定義し、推論処理をスレッドセーフなロック（`Lock`）または完全な直列（手続き型）で実行する。
    - `analyze_segment` 内でのONNX推論の並列呼び出しを排除し、同期的に処理する。
  </target>
  <target id="Librosaの圏論的Applicative & Productの設計">
    関数型プログラミング（圏論）の概念を Python に導入し、Librosa解析をクリーンに構造化する。
    - `Reader` アプリカティブに相当する `FeatureExtractor[T]` を実装する。
    - **Applicative** の定義:
      - `pure(x: T)`: 任意の値をコンテキストに包む。
      - `map(f: T -> U)`: コンテキスト内の値に関数を適用する。
      - `ap(f: FeatureExtractor[T -> U])`: コンテキストに入った関数を、コンテキストに入った値に適用する。
    - **Product**（直積）の定義:
      - 2つの `FeatureExtractor` から、それらのペアを返す新しい `FeatureExtractor` を生成する `product` 演算（または `*` などの独自演算子）を定義する。
      - 例: `FeatureExtractor[A] * FeatureExtractor[B] -> FeatureExtractor[tuple[A, B]]`
    - **合成 (Class Synthesis)**:
      - 複数の独立した `FeatureExtractor`（RMS, BPM, Chroma, Spectral Centroid, SNR etc.）を `Product` で結合し、最後にクラス（`LibrosaFeatures`）のコンストラクタへ適用（`map`）することで、最終的な機能クラスインスタンスを合成する。
      - 今後、波形分離されたテンソルが入力された際にも、この `FeatureExtractor` の入力コンテキスト（`AudioContext` または `numpy.ndarray`）を切り替えることで容易に対応可能とする。
  </target>
  <target id="共通部分式除去 (CSE) による遅延プロパティキャッシュ">
    - 特徴量抽出関数群の中で重複して実行される `librosa.stft`、`np.abs(S)`、`librosa.feature.melspectrogram`、`librosa.feature.chroma_stft` などの重い DSP 計算を、`AudioContext` クラスのプロパティ（`@property`）としてカプセル化する。
    - 各プロパティは、初回アクセス時にのみ計算を実行し、結果をプライベート属性（`self._stft`, `self._spectro` 等）にキャッシュして保持する。
    - これにより、1ソースに対して何個特徴量があっても、STFT や Melスペクトログラム等の計算は最大1回に制限され、処理速度が大幅に向上する。
  </target>
  <target id="前段: GLOBAL_DEMUCS方式と StemContext">
    - 起動時に1回だけモデルのロードを行うグローバルオブジェクト `GLOBAL_DEMUCS` （`HTDemucsSeparator` クラスのシングルトンインスタンス）を導入する。
    - 推論エンジンには `demucs-onnx` を採用し、ONNX Runtime で動作させる。
    - インフェレンス実行環境の初期化時、`onnxruntime` の `SessionOptions` で `intra_op_num_threads=1` および `inter_op_num_threads=1` を設定し、さらに DirectML 動作安定化のため以下の設定を適用する：
      ```python
      so = ort.SessionOptions()
      so.intra_op_num_threads = 1
      so.inter_op_num_threads = 1
      so.enable_mem_pattern = False
      so.enable_cpu_mem_arena = False
      ```
    - 実行プロバイダは `['CUDAExecutionProvider', 'DmlExecutionProvider', 'CPUExecutionProvider']` の優先順位で動的選択する。
    - 波形分離モデルの結果をラップする `@dataclass class StemContext` を作成し、中身を `stems: dict[str, AudioContext]` として保持する。これによりデマクス部と特徴量抽出部の結合を疎結合に保つ。
    - `demucs-onnx` が出力する各ステム（`vocals`, `drums`, `bass`, `other`）の波形データを取得し、それぞれの波形から `AudioContext` インスタンスを生成してパックする。
  </target>
  <target id="FLAC書き込み（丸め千倍）とPostgres用生データ（JSONB）の分離">
    - 解析結果を格納するデータクラス `LibrosaFeatures` / `EssentiaFeatures` は、内部的にすべて生の `float`（または `str`/`int`）型でデータを保持する。
    - 以下のインターフェースを実装する：
      - `to_flac_tags() -> dict[str, str]`: 従来の仕様に従い、値を丸め（100倍/1000倍）して文字列化したFLAC用タグ辞書を生成。
      - `to_postgres_dict() -> dict[str, Any]`: データベース用の構造を生成。今後の特徴量追加でテーブル定義 (DDL) 変更を不要にするため、特徴量マップ全体をカプセル化した `features`（Postgres側の `JSONB` 相当）と `source` に構成する。
  </target>
  <target id="新特徴量: LIBROSA_HNR (調波対雑音比) の追加 [PLANNED]">
    - 楽器の少なさや独唱の澄み具合、ランダムな波形の少なさを $0.0 \sim 1.0$ の数値で定量評価するため、正規化自己相関ピーク（Normalized Autocorrelation Peak）に基づく `LIBROSA_HNR` を追加する。
    - **計算アルゴリズム**:
      - 信号の自己相関 $R(\tau)$ を FFT または直接相関によって高速に算出する。
      - ピッチ周期の妥当な探索範囲（例: F0下限 50Hz から上限 2000Hz に対応するサンプルの遅延ラグ）において、自己相関値の最大ピーク $R(\tau_{max})$ を探索する。
      - $R(\tau_{max}) / R(0)$（全エネルギーに対する倍音エネルギー比率）を算出し、これを $0.0 \sim 1.0$ の範囲にクランプして `LIBROSA_HNR` 値とする。
  </target>
  <target id="分離波形ベースの SNR 算出設計 (アプローチ A / B の机上設計)">
    従来のプレエンファシスによる簡易的な SNR を廃止し、波形分離されたステムを利用して音楽的および品質的な SNR / SDR を求める設計。
    
    ### アプローチ A: 他ステムの総和をノイズとする「ステム相対 SNR (Vocal/Instrumental Dominance)」
    あるターゲットステム $y_{\text{target}}$ に対して、それ以外の全ステム（$mix$ を除く）の総和をノイズ（伴奏または競合パート）と定義し、音圧比率を算出して最終的に $[0.0, 1.0]$ の範囲の `float` として FLACタグ/Postgres に埋め込む。
    
    #### 1. 0-1 float への正規化（スケーリング）手法
    デシベル単位の無制限な値から $[0.0, 1.0]$ の有限範囲へマップするため、以下のいずれかの方針を採用する：
    - **方針A1: エネルギー比率（パワー比）ベース（推奨）**
      ターゲットステムのパワーが、全体の分離ステムパワー総和に対して占める割合を直接算出する。直感的であり、かつ数学的に追加処理なしで確実に $[0.0, 1.0]$ に収まる。
      $$\text{SNR}_{01} = \frac{\text{mean}(y_{\text{target}}^2) + \epsilon}{\sum_{k \neq \text{"mix"}} \text{mean}(y_k^2) + 2\epsilon}$$
    - **方針A2: ロジスティック・シグモイド関数による非線形スケーリング**
      算出されたデシベル値 $\text{SNR}_{\text{dB}}$ に対し、シグモイド関数を適用してなだらかに $[0.0, 1.0]$ へ圧縮する。
      $$\text{SNR}_{01} = \frac{1}{1 + e^{-\alpha \cdot \text{SNR}_{\text{dB}}}}$$
      （$\alpha$ はスケール感度調整係数。例: $0.1$）
    - **方針A3: dB値の線形クランプ (Min-Max スケーリング)**
      実用的なデシベル範囲（例: $-20\text{ dB}$ から $+20\text{ dB}$）を設定し、その範囲を $[0.0, 1.0]$ に線形写像し、範囲外は $0.0$ / $1.0$ にクランプする。
      $$\text{SNR}_{\text{dB}} = 10 \cdot \log_{10} \left( \frac{\text{mean}(y_{\text{target}}^2) + \epsilon}{\text{mean}(y_{\text{other}}^2) + \epsilon} \right)$$
      $$\text{SNR}_{01} = \text{clip} \left( \frac{\text{SNR}_{\text{dB}} - \text{SNR}_{\min}}{\text{SNR}_{\max} - \text{SNR}_{\min}}, 0.0, 1.0 \right)$$
    
    #### 2. 圏論的破綻のない「後処理オーバーライト（Post-Bind Overwrite）」設計
    `FeatureExtractor` が計算中に他ステムの情報を欲することは、コンテキスト結合度を高め圏論的な抽象化（共通の Applicative 適用）を損なうため避けるべきである。
    これを完全に回避するため、並列抽出・Product合成が完了した後の**同期後処理フェーズ（Post-processing Phase）**において、得られた `LibrosaFeatures` の結果オブジェクトに対して個別 SNR を計算し、上書き（オーバーライト）する設計を採用する。
    
    ##### 処理フロー
    1. **並列 Product 抽出フェーズ**:
       - `librosa_extractor.run(ctx)` を mix および各ステムに対して並列実行する。
       - この際、mix は従来の簡易 SNR（プレエンファシスによる対数比）を算出し、各ステムは簡易 SNR を計算するか、あるいはプレースホルダー（ダミー値）を出力する。
    2. **エネルギー比率（0-1 SNR）の同期後計算**:
       - 全スレッドが合流し、`track_features: dict[str, LibrosaFeatures]` が回収された直後、すでに抽出済みの `energy` 特徴量（波形の RMS 平均エネルギー）を利用して、各ステムの 0-1 SNR を算出する。
       - すでに DSP 計算が完了した `energy` プロパティを再利用するため、余計な波形走査や STFT 計算のオーバーヘッドは **ゼロ** である。
       - 計算式（オケの総和に対する割合）:
         $$\text{total\_energy} = \sum_{k \neq \text{"mix"}} \text{track\_features}[k].\text{energy}$$
         $$\text{vocals\_snr\_01} = \frac{\text{track\_features["vocals"]}.\text{energy} + \epsilon}{\text{total\_energy} + 2\epsilon}$$
    3. **プロパティの上書き（オーバーライト）**:
       - 算出した 0-1 スケールの float 値を、各ステムの `LibrosaFeatures.snr` 属性へ直接上書き（オーバーライト）する。
         ```python
         track_features["vocals"].snr = vocals_snr_01
         ```
       - mix 側の `snr` は上書きせず、従来の簡易指標をそのまま維持する。
    4. **タグ書き込み & DB送信バッファ保管**:
       - 上書き完了後、`to_flac_tags()` による FLAC メタデータ書き戻しと、PostgreSQL 送信バッファへの JSONB シリアライズ処理を一挙に実行する。
    
    ##### 効果
    * **圏論的純粋性の維持**: `FeatureExtractor` および `AudioContext` のクラス構造やパイプライン処理フローを一切改変する必要がなく、合成完了後の平坦なデータクラスを操作するだけで完結する。
    * **パフォーマンス最適化**: 重い波形ループを回し直すことなく、すでに計算された単一の float 特徴量 `energy` のみを用いて $O(1)$ で SNR が求まるため、極めて高速かつ省メモリである。
    
    ### アプローチ B: オリジナル波形との残差をノイズとする「分離品質 SDR (Signal-to-Distortion Ratio)」
    モデルの分離誤差（歪みやリーク）をノイズと定義し、純粋な分離精度を評価する。
    - **計算式**:
      $$e_{\text{dist}} = y_{\text{mix}} - \sum_{k \neq \text{"mix"}} y_k$$
      $$\text{SDR}_{\text{stem}} = 10 \cdot \log_{10} \left( \frac{\text{mean}(y_{\text{target}}^2)}{\text{mean}(e_{\text{dist}}^2) + \epsilon} \right)$$
    - **特徴**:
      - 分離処理に伴う音質劣化や合成損失の定量評価には役立つが、市販FLACの音楽的特徴量としては「アプローチ A」の方がプレイリスト作成やミックス分析に対する実用価値が圧倒的に高い。よって、アプローチ A を優先採用する。
  </target>
  <target id="Librosa音楽特徴量の強化に関する詳細設計">
    MIR（Music Information Retrieval）的価値を高めるため、以下の特徴量を新設計パイプラインに組み込む。
    
    ### 1. Chroma 12次元詳細
    - **計算方法**:
      - 既存の `ctx.chroma` (`shape: (12, t)`) は、STFTスペクトログラムから抽出された各フレームにおける12半音の分布である。
      - 各半音成分（C, C#, D, ..., B）の時間平均を算出し、これを12次元ベクトルとして保存する。
    - **データ表現**:
      - `chroma_c`, `chroma_c_sharp`, `chroma_d`, `chroma_d_sharp`, `chroma_e`, `chroma_f`, `chroma_f_sharp`, `chroma_g`, `chroma_g_sharp`, `chroma_a`, `chroma_a_sharp`, `chroma_b` の12個の独立した浮動小数点数。
      - FLACタグ: `LIBROSA_CHROMA_C`〜`LIBROSA_CHROMA_B` (値を100倍して整数化)。
      - PostgreSQL JSONB: `chroma_c`〜`chroma_b`。
    
    ### 2. Harmonic/Percussive Ratio (HPSS)
    - **計算方法**:
      - `AudioContext` の波形 `y` に対し、`librosa.effects.hpss(y)` を用いて Harmonic（調波）成分波形 `harmonic_wave` と Percussive（打楽器・過渡）成分波形 `percussive_wave` に分離する。
      - それぞれのエネルギー（平均二乗値）を求める：
        - $E_{\text{harmonic}} = \text{mean}(y_{\text{harmonic}}^2)$
        - $E_{\text{percussive}} = \text{mean}(y_{\text{percussive}}^2)$
      - 占有比率（Ratio）:
        - $\text{ratio}_{\text{percussive}} = \frac{E_{\text{percussive}}}{E_{\text{harmonic}} + E_{\text{percussive}} + \epsilon}$
    - **第二世代キャッシュ層 (AudioContext)**:
      - `@property def hpss(self)` を定義し、`(harmonic_wave, percussive_wave)` のペアを遅延キャッシュする。
    
    ### 3. Spectral Flux
    - **計算方法**:
      - スペクトログラムの時間差分を取り、音色の展開量・変化量を測定する。
      - $D[f, t] = |S[f, t] - S[f, t-1]|$ (ここで $S$ は `ctx.spectro`)
      - 各フレーム $t$ における Flux: $F[t] = \text{mean}(D[:, t])$
      - 統計量として時間平均 $F_{\text{mean}}$ と標準偏差 $F_{\text{sd}}$ を算出する。
    
    ### 4. Onset Density
    - **計算方法**:
      - `AudioContext` に `@property def onset_env(self)` を実装。
      - `log_mel = librosa.power_to_db(self.mel, ref=np.max)` を利用し、`librosa.onset.onset_strength(S=log_mel, sr=self.sr)` によって Onset エンベロープを計算（既存 Mel キャッシュを再利用し高速化）。
      - `librosa.onset.onset_detect(onset_envelope=onset_env, sr=self.sr)` によって onset 位置をフレーム単位で検出。
      - $\text{density} = \frac{\text{onsetの総数}}{\text{曲の長さ (秒)}}$。
      - 激しい曲や打撃音の多い曲の分類に威力を発揮する。
    
    ### 5. Tempogram統計
    - **計算方法**:
      - `AudioContext` に `@property def tempogram(self)` を実装。
      - `librosa.feature.tempogram(onset_envelope=self.onset_env, sr=self.sr)` を計算。
      - テンポ安定性（Stability）: 各フレームにおけるテンポ候補の最大強度の時間平均。
        - $\text{stability} = \text{mean}(\max(\text{tempogram}, \text{axis}=0))$
      - テンポ変動（Variation）: 各フレームでの最大テンポラグ（インデックス）の標準偏差。
        - $\text{variation} = \text{std}(\text{argmax}(\text{tempogram}, \text{axis}=0))$
      - ライブ音源とグリッドに沿った打ち込み音源の分離に効果的である。
    
    ### 6. Dynamic Range
    - **計算方法**:
      - $RMS[t]$ (`librosa.feature.rms(S=ctx.spectro)`) をデシベル変換：$RMS_{\text{dB}}[t] = 20 \cdot \log_{10}(RMS[t] + \epsilon)$
      - 95パーセンタイルと5パーセンタイルの差分を取ることで、突発的なピークや無音部を除いた音楽的ダイナミックレンジを定量化する：
        - $\text{DR} = \text{Percentile}(RMS_{\text{dB}}, 95) - \text{Percentile}(RMS_{\text{dB}}, 5)$
    
    ### 7. MFCC増量 (n_mfcc = 20)
    - 既存の 8次元から 20次元へ拡張。
    - `to_flac_tags()` において `LIBROSA_MFCC00`〜`LIBROSA_MFCC19` を出力し、`to_postgres_dict()` でも 20次元の配列として格納する。
  </target>
  <target id="CoMonad方式による波形ハッシュ (audio_hash) 設計">
    8万曲強のライブラリにおいて、Embedded CUE などのトラック分割やタグ更新に耐え、かつ圏論的な抽象化を保つためのハッシュ値生成アプローチ。
    
    ### 1. 概念設計: コモナド恒等元としてのハッシュ
    - `AudioContext` を波形データとその計算キャッシュを表すコモナド的なコンテキスト（環境）とみなす。
    - 音響特徴量抽出 (`FeatureExtractor`) は DSP（デジタル信号処理）ドメインに専念させ、ハッシュ値というシステム識別用のメタデータ計算を混入させない（意味論的汚染の防止）。
    - ハッシュ値は `AudioContext` が保持するモノラル波形 $y$ から決定論的に導出されるため、`AudioContext.audio_hash` プロパティとして遅延キャッシュ処理する。
    
    ### 2. 計算アルゴリズム
    - 浮動小数点数（`float32`）配列である波形データ `y` に対して `y.tobytes()` を呼び出し、得られたバイト列から MD5 ハッシュ値を生成する。
    - 1つのFLACファイルから複数のCUEセグメントが切り出される場合、各セグメントの `seg_audio` 配列からハッシュが算出されるため、Embedded CUE内のトラック同士でも重複しない一意のIDが自然に生成される。
    - メタデータ（タグ）の更新を行ってもデコードされる波形データは不変であるため、このハッシュ値はタグ書き換えによる影響を一切受けない。
    
    ### 3. パイプラインとの統合
    - `analyze_segment_pipeline` の戻り値を `tuple[dict[str, LibrosaFeatures], EssentiaFeatures | None, str]` に拡張し、第3引数として `mix` ステムの波形ハッシュを呼び出し側に返す。
    - 呼び出し側（`analyze.py`）はこのハッシュを `raw.library_flac` の主キー（`audio_hash`）として用い、同一のハッシュが存在する場合はタグ情報 (`meta`) のみの UPDATE を行う高速スキャン処理を実現する。
  </target>
  <target id="Cuesheet 複数箇所統合パース ＆ 平坦化カラムスキーマ v2 設計">
    ### 1. 堅牢な Cuesheet 抽出設計
    Embedded CUE ファイルにおける Cuesheet メタデータの取得性を極限まで高めるため、以下の3重フォールバック設計を導入。
    1. **テキストメタデータ**: Vorbis comment 内の `"cuesheet"` タグを検索・取得し、`parse_cue_segments` にて秒数・サンプル数の境界セグメントリストを構築。
    2. **バイナリメタデータ**: cuesheet テキストが検出できない場合、FLAC ヘッダー内の `METADATA_BLOCK_CUESHEET` ブロックから各トラックの `track_number` と `start_offset` を回収し境界を特定。
    3. **個別タグの逆引き**: 上記の境界情報も無い場合、タグキーを走査して `cue_trackXX_` や `trackXX_` 個別タグの存在からトラック番号リストを自動逆引き検出。
    
    ### 2. 個別タグのマージ・フォールバック
    CUEシート内の記述とFLACの個別トラックタグ（例: `cue_track01_title`, `CUE_TRACK01_TITLE`, `cue_track1_title` 等）が混在する「複数格納場所」問題を解決するため、大文字小文字・0パディングの差異を lower キーマッピング処理で正規化。
    優先順位（CUEシート内 ➔ 個別タグ ➔ ファイル全体のグローバルタグ ➔ システムフォールバック）に従ってタイトル・アーティスト・コンポーザーを決定し、`meta["cuesheet"]` JSONB 構造へ集約する。
    
    ### 3. 平坦化スキーマ v2 による検索性向上
    PostgreSQL テーブル設計において、JSONB 式インデックスのオーバーヘッドを避け、直接インデックス検索を可能にするための「平坦化検索用カラム」を `raw.library_flac` に新設。
    - `album_artist` : アルバムの代表アーティスト
    - `album` : アルバム名
    - `artist` : トラックのアーティスト (Cuesheet 由来またはグローバルフォールバック)
    - `title` : 曲名 (Cuesheet 由来またはグローバルフォールバック)
    - `track_number` : トラック番号 (`track_num` から名称変更)
    - `filepath` : `os.path.abspath(filepath)` による絶対パス固定
    
    これら平坦化カラムに対して個別に B-Tree インデックスを定義し、高速な検索性能を保証する。
    また、トリガー関数 `raw.archive_library_flac_history()` をこれらの新規カラム追従型に再定義し、履歴退避処理の整合性を担保。
  </target>
  <target id="ステレオ優先遅延モノラル化に伴う Essentia 入力の downmix ガード">
    - 音響的なパン振り情報を活かして分離精度を劇的に向上させるため、パイプラインの最初でモノラル化せず、ステレオで Demucs 処理を行うようにリファクタリング。
    - このため、`pipeline.py` で Essentia 用の特徴抽出 `models.extract_mel_patches` にステレオ波形がそのまま渡される。
    - `librosa` (0.9.0以降) は多次元 `(samples, channels)` を `(channels, samples)` として誤認するため、`extract_mel_patches` の最初で必ずモノラル化（downmix）を行うガードを実装し、`UserWarning: n_fft=512 is too large for input signal of length=2` を完全に排除した。
  </target>
  <target id="mutagenメタデータ全マージと個別トラックフィルタリングの設計">
    - **設計の命題**: mutagenで取得した全メタデータタグを Postgres の `meta` JSONB へ完全マージする際、マルチトラック（Cuesheet分割）処理時における他トラックの個別タグ混入を防ぎ、かつシングルトラック時とマルチトラック時でデータベースのキー構造（スキーマ）を綺麗に統一する。
    - **圏論的アプローチ**:
      - メタデータ抽出を「FLAC VorbisComment という対象（Object）から、Postgresの `meta` JSONB という対象（Object）への射（Morphism）」として捉える。
      - すべて of テキストメタデータタグの構造を完全に保存して写像する。
      - `CUE_TRACK_XX_...` という個別トラックプレフィックス付きのタグについて、自トラック `XX` 以外のタグをフィルターし、自トラックのタグはプレフィックスを除去して共通のタグ型（型定義空間）へマッピングする操作は、「シグモイド（切り出し）作用素」に付随する「キー名の正規化（同値関係による商対象の構成）」に相当する。
      - これにより、シングルトラックとマルチトラックの双方において、DB内の `meta` スキーマ（JSONB内の構造）が同じ対象へと射影されるため、データベースを処理するクエリの定義域が統一され、圏論的整合性が保たれる。
    - **値の平坦化変換**:
      - VorbisCommentの複数値リスト構造について、要素数が1つの場合は文字列（`str`）として平坦化し、複数ある場合のみリストのまま格納する動的写像を定義し、JSONシリアライズの可読性と互換性を向上させる。
  </target>
  <target id="Demucsステム (drums, bass) への tempobeat 抽出拡張とキャッシュ整合の設計">
    - **設計の命題**:
      - 音源分離によって得られた `drums`（ドラム）および `bass`（ベース）のステムに対して、ビートトラッキング（`tempobeat` = BPMおよびビート位置）の抽出を可能にし、それぞれのビート規則性や Groove 特徴量を分析・データベースに保存できるようにする。
      - 同時に、スレッド並列処理下における GIL および `LIBROSA_LOCK` 競合を回避し、システムの実行時パフォーマンスを損なわない設計とする。
    - **圏論的アプローチ**:
      - `FeatureExtractor[T]`（環境 $C$ から値 $T$ への射 $C \to T$）における定義域の制限を緩和する。
      - 旧設計では、 `AudioContext.tempobeat` は `source != "mix"` の場合に一律でダミー値 `(0.0, [])` を返していた。これは、無駄なステムのビートトラッキングを回避するためのコンテキスト依存の射の制限であった。
      - 今回、 `drums` と `bass` についてビートトラッキングを有効化するため、条件を `self.source in ("mix", "drums", "bass")` に拡張する。これは部分射の定義域を論理的に包含する操作であり、Applicative (Reader) の Product（直積）合成における余ドメインの代数構造および射の合成性（Compositionality）を完全に保存する。
    - **パフォーマンスと並列性の整合 (Strictification)**:
      - `pipeline.py` における事前キャッシュ（Pre-warming）において、 `drums` および `bass` の `tempobeat`, `onset_env`, `tempogram` を直列フェーズで強制評価（Strict Evaluation）しキャッシュに格納する。
      - スレッド並列実行時にオンデマンドで重い DSP 計算が走り、 `LIBROSA_LOCK` によるスレッドのブロッキングや CPU/RAM リソースの無駄なオーバーヘッドが発生するのを防ぐ。これにより、圏論的な遅延プロパティの評価（Lazy Evaluation）と、並列処理の安全性（Concurrency Safety）が調和する。
  </target>
</methods>
</details>
### 2026-06-22 22:56 > BugFix/SharedMemory leak & NameError/load_wave.py, pipeline.py
- [load_wave.py](file:///a:/Users/letwir/repo/flac_analyzer/load_wave.py): `_SHM_KEEP_ALIVE` を FIFO キャッシュ方式に変更。保持するトラック数の上限を64とし、超えた場合は最も古いトラックの SharedMemory オブジェクトを Producer 側でクローズ・解放するように修正。
- [pipeline.py](file:///a:/Users/letwir/repo/flac_analyzer/pipeline.py): `import time` を追加し、待機処理内の `time.sleep` での `NameError` を解消。
- ユニットテスト (`pytest tests/`) およびテストバッチの実行により、修正後の正常動作とリーク防止を検証。

### 2026-06-25 08:05:00 > Architecture/1ファイルインプロセス解析への大改修による RAM OOM 制圧/run_batch.ps1, main.py, pipeline.py
- [x] DONE: `run_batch.ps1` において、対象 FLAC ファイルの再帰的列挙と配列（一時保存）による1ファイルずつのループ同期呼び出しへの移行を実装。
- [x] DONE: `run_batch.ps1` に `log_メインフォルダ__サブフォルダ.log` からの成功ファイルパスを `HashSet` 化し、Python 起動前に判定してスキップする高速スキップ機構を実装。
- [x] DONE: `main.py` のコマンドライン引数を `directory` から単一の `filepath` に変更し、不要になった `Producer-Consumer` などのマルチプロセス並列実行処理を完全に撤廃。
- [x] DONE: `pipeline.py` に、インプロセスで「デコード → 波形分離 → 特徴量抽出 → DB書き込み (UPSERT) → タグ更新」を安全に直列で完結させる `process_single_flac_file_directly` 関数を新規実装。
- [x] DONE: `pytest` による自動テスト、および `-Test -Skip` によるテストモード手動検証がすべて正常に動作・パスすることを確認。

### 2026-06-28 01:46:57
> [x] DONE
> Category: Orchestration
> Summary: Implemented Go orchestrator base (HTTP server & Goroutine worker pool) and updated run_batch.ps1 to enqueue tasks via POST. Verified dummy workflow.
> Files: run_batch.ps1, orchestrator/main.go, orchestrator/go.mod, issues.md

### 2026-06-28 01:51:00 > Implementation/WORM Shared Memory/orchestrator/shm_windows.go, orchestrator/shm_windows_test.go

### 2026-06-27 16:56:00
[~] IMPLEMENTED
Summary: Python 既存の db.py 依存を完全に切断し、解析結果（features, meta）を JSON Lines として標準出力へ返すロジックへのリファクタリング。
Files: pipeline.py, main.py
### 2026-06-27 17:00:00
[~] IMPLEMENTED
Summary: Python 側の psycopg2 依存を全排除。db.py および verify_db_connection.py を git rm で完全削除。
Files: pipeline.py, db.py, verify_db_connection.py
### 2026-06-27 17:05:00
[~] IMPLEMENTED
Summary: Go オーケストレーターに `--no-db` フラグを追加し、テスト時に PostgreSQL UPSERT を無効化してローカルの JSON ファイル (`testFLAC/*.json`) へ出力を保存する機能を実装。
Files: orchestrator/main.go### 2026-06-29 16:41:19 > Python Zero-copy Pipeline Integration / Completed Go-Python orchestrator binding, enabled absolute paths for python execution.
Files: orchestrator/main.go, pipeline.py, run_batch.ps1

### 2026-06-29 16:44:47 > IMPLEMENTED/Integration test/test_integration.py
### 2026-06-30 23:56:42
Category: Bugfix
Summary: Fixed cwd resolution in orchestrator, resolved console encoding (mojibake) via Windows API, and fixed SHM access denied error caused by Get-Item bracket parsing.
Files: run_batch.ps1, orchestrator/main.go

### 2026-06-30 23:59:07
> Category: Code/Modification
> Summary: Modified models.py to cache Demucs ONNX model locally in demucs folder instead of redownloading.
> Files: models.py
### 2026-07-01 00:28:00
> Category: Implement / Fix
> Summary: Implemented Scipy features (Skewness/Kurtosis, Hilbert envelope, peaks), and fixed missing FLAC tag prefixes for Essentia/Demucs by using consistent CUE_TRACKXX_ prefix.
> Files: analyzer.py, pipeline.py

### 2026-07-01 06:52:36 > BugFix/Fixed UPSERT ignoring predictions column/ingester.py

### 2026-07-01 07:19:24 > BugFix/Fixed orchestrator ingester.py invocation (use pythonPath, append envVars, capture logs)/orchestrator/main.go

### 2026-07-10 10:07:00 > Add DB ER Diagram/ER図（Markdown+Mermaid）をドキュメントとして追加/docs/database_er_diagram.md

### 2026-07-16 08:15:05
- [x] DONE: 中期目標（Go Orchestrator & DLQ 安定化）に関する詳細検討書（懸念点、破滅的改変の可能性、犠牲要素）を作成し、旦那様へ提示。

### 2026-07-17 04:40:00
- [x] DONE: Goソースのビルド検証 (`go build`) と単体テスト (`go test ./...`) のパス確認。

### 2026-07-17 04:45:00
- [x] DONE: 古くて不要になったスクリプト群（`patch.py`, `extract_cue.py`, `refactor_db.py`, `fix_pipeline_db.py`, `test_db.py`, `test.py`, `test2.py`, `test3.py`, `test_payload.json`, `run_batch.sh`）を Git から削除し、ソースのクリーンアップを実施。

### 2026-07-17 05:11:00
- [x] DONE: Go Orchestrator を CGOフリーな pure Go 実装 `modernc.org/sqlite` へ移行し、Windows 環境（GCC不在）でのビルドと実行時の DB 初期化スタブクラッシュを根絶。
- [x] DONE: Go の Python 呼び出しにおいて `.venv/Scripts/python.exe` を優先アタッチするように修正し、依存モジュール（librosa 等）のロード失敗を解消。
- [x] DONE: インテグレーションテスト `test_integration.py` を、一時的 `config_test.toml` 上書きによる DB 接続テスト形式に修正し、タスクの進捗判定を SQLite `task_state` の状態カウントにすることで `ingester.py` のクリーンアップに干渉されない頑健なテストへと改善。
- [x] DONE: ダミーの極小 FLAC ファイルの自動生成・退避・復元スクリプトを用意し、CPU 推論によるテスト実行時間を数時間から 3 分台（STATUS: SUCCESS）へ劇的に最適化。

### 2026-07-17 08:45:00
- [x] DONE: Go Orchestrator にログレベル制御（コンソールのデフォルト info 出力、子プロセスのエラー行絞り込み）を実装。
- [x] DONE: Windows のアプリケーションイベントログ（EventLog）へ warn 以上のログを転送する仕組みを追加（管理者権限不足時の安全なフォールバック付き）。
- [x] DONE: Prometheus にエラー累積件数カウンター `analyzer_errors_total` を追加。
- [x] DONE: Go の dispatcher.go における `os.Executable()`, `cmd.StderrPipe()`, `json.Marshal()` 等の戻り値エラー無視（握りつぶし）を修正。
- Files: orchestrator/main.go, orchestrator/dispatcher/dispatcher.go, orchestrator/metrics/metrics.go, changeLOG_Implementation Plan.md, changeLOG_Walkthrough.md

### 2026-07-17 08:46:00
- [x] DONE: Python 側ワーカー群（worker_*.py, functor_precache.py）および ingester.py における例外発生時の logger.error を logger.exception へリファクタリングし、Go 側へエラーの詳細なスタックトレースが漏れなく伝達されるよう堅牢化。
- Files: worker_demucs.py, worker_librosa.py, worker_essentia.py, worker_tensor.py, functor_precache.py, ingester.py

### 2026-07-21 08:40:00
- Category: Refactoring & Cleanup
- Summary: Gitの不要ファイル追跡の即時是正、Python/Goのエラーハンドリング徹底、MD5ハッシュ比較による事前重複チェックおよび解析スキップロジックの導入、設定の config.toml 一元管理化、CUDA/GPUビルド手順の明文化。
- Decisions:
  - SQLiteの DB ファイルや一時 json 等がコミットに含まれないよう `.gitignore` に追加し、`git rm --cached` で追跡を解除。
  - 特徴量抽出中の例外がスタックトレースなしで警告だけになっていた箇所を `logging.exception` に修正。Go側の一時ファイル書き込みエラーチェックを追加。
  - 軽量デコードにより `audio_hash` を算出し、`ingester.py --check-hash` を介して PostgreSQL に問い合わせることで、すでにDBに登録済みの曲は Demucs 分離や Librosa 解析を丸ごとスキップするバイパス処理を Go Orchestrator に実装。
  - 事前ハッシュ重複チェックの ON/OFF を `config.toml` 内の `skip_dup_by_hash` から動的に制御できるように Go 側へ統合。
  - `retry_ingest.py` の DB 接続先 URL 取得順序を `config.toml` 最優先に変更し、ローカルの Postgres はテスト用として扱い、動作設定は極力 `config.toml` に一元管理する方針を `method.md` に明記。
- Files: .gitignore, ingester.py, worker_demucs.py, orchestrator/main.go, orchestrator/dispatcher/dispatcher.go, retry_ingest.py, config.toml, method.md, analyzer.py, pipeline.py, requirements.txt, README.md

### 2026-07-22 08:15:00
- Category: Documentation / Refactoring
- Summary: README.md の構成再構築と圏論用語の完全排除、日本語・英語の二言語並記化。
- Decisions:
  - 概要、必要なもの、使い方 (USAGE)、状態図 (Mermaid)、ER図およびJSONB構造の順序で構成を統一。
  - 圏論的用語（射、コモナド、アプリーカティブ等）を全て平易なシステムエンジニアリング用語へ置換。
  - 前半に日本語ドキュメントを配置し、`---` (区切り) の後に英語ドキュメントを並記。
- Files: README.md

### 2026-07-22 08:22:00
- Category: Licensing
- Summary: リポジトリライセンスを AGPLv3 から MIT License に変更。ONNXモデルの個別ライセンスに関する留意事項の追加。
- Decisions:
  - リポジトリのソースコード自体は MIT License を適用。
  - LICENSE ファイルおよび README.md (JA/EN) に、ダウンロード・使用する外部 ONNX モデル (Essentia / Discogs / Demucs 等の AGPLv3 / CC ライセンス) に対する注意書き (Warning Notice) を追加。
- Files: LICENSE, README.md

### 2026-07-22 08:27:00
- Category: Repository Cleanup & Configuration
- Summary: `git-filter-repo` を使用して `search/` ディレクトリを Git 全履歴から削除し、`.gitignore` に `demucs/` および `search/` を明記。
- Decisions:
  - `.gitignore` に `search/` を追記。
  - `git-filter-repo --path search --invert-paths --force` を実行し、`search/` の履歴を完全抹消。
- Files: .gitignore, .git (History rewritten)

0
### 2026-07-23 22:56:00
- Category: BugFix
- Summary: Fix AttributeError caused by non-existent ort.set_default_logger_severity
- Decisions: Replaced invalid attribute with os.environ[\ ORT_LOGGING_LEVEL\] = \n- Blockers: None
- Files: models.py

### 2026-07-24 00:26:00
- Category: BugFix
- Summary: Prevent Ingester failure and DLQ fallback by truncating long metadata string fields to 255 characters
- Decisions: Added [:255] string truncation in ingester.py and retry_ingest.py for album, title, artist, and album_artist fields. Created models/.gitkeep
- Blockers: None
- Files: ingester.py, retry_ingest.py, models/.gitkeep
