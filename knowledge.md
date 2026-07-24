<knowledge>
  <api id="ONNX_CONCURRENCY">
    <title>ONNX Runtime Concurrency and Segmentation Faults</title>
    - **概要**: `onnxruntime` の C++ バックエンドは内部で OpenMP またはスレッドプールを使用して演算を並列化している。Python の `threading` または `concurrent.futures` から同一または複数の `InferenceSession` に対して並列に `.run()` を実行すると、スレッド競合やスタックの破壊が発生し、Segmentation Fault が生じることがある。
    - **対策**: 
      - `intra_op_num_threads=1` および `inter_op_num_threads=1` を設定し、セッション作成時に並列化を無効化する。
      - 推論処理自体をグローバルな排他ロック（`threading.Lock`）で囲み、完全に直列化（シングルスレッドアクセス）する。
    - **参照**: [ONNX Runtime API Reference](https://onnxruntime.ai/docs/api/python/api_summary.html)
  </api>
  <api id="APPLICATIVE_FUNCTOR">
    <title>Applicative Functors and Products in Python</title>
    - **概要**: 圏論における Applicative Functor は、`Functor`（`map` を持つ）を拡張し、コンテキストに包まれた関数をコンテキストに包まれた値に適用する `ap`（または `lift_a2` などのユーティリティ）を提供する。
    - **Pythonでの実装**:
      - `FeatureExtractor[T]` を `Reader` モナド（あるいは単なる Applicative）として表現する。
      - `map` (Fmap): `f: A -> B` を適用して `FeatureExtractor[B]` を作る。
      - `ap`: `FeatureExtractor[Callable[[A], B]]` を用いて、`FeatureExtractor[A]` を `FeatureExtractor[B]` に変換する。
      - `product`: `FeatureExtractor[A]` と `FeatureExtractor[B]` を合成して `FeatureExtractor[tuple[A, B]]` を作る。これは `ap` と `map` を用いて以下のように定義できる：
        `product(fa, fb) = fb.ap(fa.map(lambda a: lambda b: (a, b)))`
      - これにより、独立した解析結果を tuple にまとめ、最終的に `LibrosaFeatures(*args)` のようにクラスのコンストラクタ（`pure` な関数）に適用することで、合成したクラスインスタンスを得ることができる。
  </api>
  <api id="HDEMUCS_INFERENCE">
    <title>Hybrid Demucs Inference: TorchAudio vs ONNX Runtime with DirectML</title>
    - **概要**:
      - **TorchAudio (PyTorch)**: `torchaudio.models.HDemucs` や公式プリセットパイプラインを使用。RTX 3060 (CUDA) では極めて高速だが、Windows上のAMD iGPU (Ryzen) でのGPU加速実行には `torch-directml` の導入が必要であり、依存関係やバージョンの競合から環境構築・維持が極めて困難である。また、PyTorch自体のインストール容量が非常に大きい（数GB規模）。
      - **ONNX Runtime (`demucs-onnx`)**: サードパーティの `demucs-onnx` ライブラリを使用。PyTorchのインストールが不要で、CPU推論時でもPyTorch比で約1.3倍高速に動作する。さらに、`onnxruntime-directml` (DmlExecutionProvider) を用いることで、Windows上のAMD iGPU (Ryzen) やNVIDIA GPU (RTX 3060) の両方で、DirectX 12を介したGPU加速推論が同一コードで容易に実行可能。
    - **実装方針**:
      - `onnxruntime` の ExecutionProvider 設定として、`CUDAExecutionProvider`, `DmlExecutionProvider`, `CPUExecutionProvider` の優先順位で動的にロードするフォールバック構造を実装する。これにより、RTX 3060環境ではCUDA、Ryzen環境ではDirectML、どちらもない場合はCPUで動作する。
    - **参照**:
      - [Music Source Separation with Hybrid Demucs - TorchAudio](https://pytorch.org/audio/stable/tutorials/hybrid_demucs_tutorial.html)
      - [demucs-onnx - PyPI](https://pypi.org/project/demucs-onnx/)
      - [ONNX Runtime DirectML Execution Provider](https://onnxruntime.ai/docs/execution-providers/DirectML-ExecutionProvider.html)
  </api>
  <api id="DIRECTML_PREPARATION">
    <title>DirectML Setup and Precautions on Windows</title>
    - **システム要件**:
      - **OS**: Windows 10 (version 1903以降) または Windows 11
      - **GPU**: DirectX 12 互換GPU（NVIDIA RTX 3060 および AMD Ryzen iGPU (Radeon) は双方とも完全対応）
      - **ランタイム**: Visual C++ 2019 再頒布可能パッケージ (MSVC runtime)
    - **環境構築上の注意点**:
      - **パッケージの競合**: `onnxruntime-directml` は `onnxruntime` (CPU) や `onnxruntime-gpu` (CUDA) と同じPython環境に共存させてはならない。共存した場合、インポート競合や実行時エラーの原因となる。必ず既存 of `onnxruntime` などをアンインストールした上で `onnxruntime-directml` のみを入れること。
      - **GPU加速の一本化**: `onnxruntime-directml` を使用すると、RTX 3060 と Ryzen iGPU の両方で `DmlExecutionProvider` を介したGPU加速推論が可能になるため、複雑な CUDA/cuDNN のセットアップなしにGPU実行が可能となる。
    - **DirectMLの安定化設定**:
      - 一部のONNXモデルや環境では、セッション作成時に以下のメモリ制限・パターン設定を無効化することでクラッシュを防止できる。
        ```python
        so = ort.SessionOptions()
        so.enable_mem_pattern = False
        so.enable_cpu_mem_arena = False
        ```
  </api>
  <api id="SBIC_SEGMENTATION">
    <title>Essentia SBic による音楽セグメンテーション (BIC基準)</title>
    - **概要**: Bayesian Information Criterion (BIC) を利用した音楽構造セグメンテーションアルゴリズム。フレーム特徴量行列（MFCC等）を入力とし、統計分布が変化する境界点を検出する。
    - **参照論文**: Lourdes Aguilera, Xavier Anguera et al. "BIC-based speaker segmentation" (ISCA)
    - **アルゴリズム3フェーズ**:
      1. Coarse Segmentation: size1, inc1 パラメータで粗い境界検出
      2. Fine Segmentation: size2, inc2 パラメータで局所精緻化
      3. Validation: minLength で短すぎるセグメントをフィルタ
    - **ΔBICの原理**:
      - H1: 2セグメントが同一ガウス分布に属する（境界なし）
      - H2: 2セグメントが異なるガウス分布に属する（境界あり）
      - ΔBIC > 0 → 境界検出。cpw (complexity penalty weight) でペナルティ調整
      - 数式: ΔBIC = (N1+N2)/2 * log|Σ| - N1/2 * log|Σ1| - N2/2 * log|Σ2| - cpw * λ
        ここで λ = 1/2 * (d + d(d+1)/2) * log(N1+N2)
    - **Essentia Python APIパラメータ**:
      `python
      import essentia.standard as es
      sbic = es.SBic(
          size1=300,     # 第1パス窓サイズ (フレーム数)
          inc1=60,       # 第1パス増分 (フレーム数)
          size2=200,     # 第2パス窓サイズ (フレーム数)
          inc2=20,       # 第2パス増分 (フレーム数)
          cpw=1.5,       # 複雑度ペナルティ重み
          minLength=10   # セグメント最小長 (フレーム数)
      )
      boundaries = sbic(mfcc_matrix)  # shape: (num_frames, num_features)
      `
    - **時間変換**: 	ime_sec = frame_idx * hop_length / sample_rate
    - **注意**: SBicは音響変化境界を検出するだけで、verse/chorus等のラベル付けは別処理が必要
    - **参照**: https://essentia.upf.edu/reference/std_SBic.html
  </api>
  <api id="SYNCOPATION_INDEX">
    <title>シンコペーション指標とGroove測定 (Witek 2014)</title>
    - **概要**: リズムの複雑さを定量化する指標。「弱拍での音符発音 + 後続強拍での休符」でシンコペーション発生。
    - **参照論文**: Witek, M.A.G. et al. (2014) "Syncopation, Body-Movement and Pleasure in Groove Music." *PLoS ONE*
      - 中程度のシンコペーションが最高grooveスコア（逆U字型関係）
    - **指標の計算アルゴリズム** (Longuet-Higgins & Lee ベース):
      1. 拍子グリッドの定義: 4/4拍子なら16分音符単位で16ポジション
      2. Metric Weight割り当て: 強拍(1beat)=5, 次強拍(3beat)=4, 2拍・4拍=3, 8分裏=2, 16分位置=1
      3. シンコペーション検出: 音符が weight[i] の位置にあり、次の weight[i+1] > weight[i] の位置に休符
      4. 集計: SI = Σ(weight[rest] - weight[onset]) (シンコペーション発生時のみ加算)
    - **Librosaベースの近似実装**:
      - eat_track で拍位置を取得
      - onset_detect でonset位置を取得
      - beatグリッドに対するonsetのズレ量を計算 → syncopation_index
    - **Swing Ratio式**:
      SR = \frac{d_1}{d_2}
      - d1: 偶数拍のIBI (longer note)、d2: 奇数拍のIBI (shorter note)
      - SR=1.0: ストレート、SR≈2.0: トリプレットスウィング、SR>2.0: ハードスウィング
    - **参照**: https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0094446
  </api>
  <api id="HARMONY_ENTROPY">
    <title>和声複雑度とChromaエントロピー (Weiss & Müller)</title>
    - **概要**: Chroma特徴量の確率分布から調和的複雑さを定量化する指標群
    - **参照**: Weiss & Müller "Tonal Complexity and Harmonic Change" (AudioLabs Erlangen)
    - **Shannon Entropyによるハーモニー複雑度**:
      H = -\sum_{k=0}^{11} p_k \log_2(p_k)
      - p_k: Chroma k成分の時間平均正規化値 (chroma_mean / sum(chroma_mean))
      - 最大値: log2(12) ≈ 3.585 bit (全12音が均等) → 無調的
      - 最小値: 0 bit (単一ピッチクラスのみ) → 単音的
    - **Circle of Fifths複雑度**:
      - Chroma vectorを5度圏上の2次元空間に射影
      - 合力ベクトルの大きさ → 小さいほど複雑（エネルギーが分散）
      - 実装: ifths_weights = [0,7,2,9,4,11,6,1,8,3,10,5] でChromaを並べ替え
    - **Tonal Centroid変化量**:
      - フレームごとのChroma重心の時間標準偏差 → 和声変化速度の指標
  </api>
  <api id="DYNAMICS_METRICS">
    <title>ダイナミクス指標 (Crest Factor / LRA)</title>
    - **Crest Factor (クレストファクター)**:
      CF_{dB} = 20 \log_{10}\left(\frac{x_{peak}}{x_{rms}}\right)
      - または線形: CF = max(|y|) / sqrt(mean(y^2))
      - 高いCF: ダイナミックで打撃感あり / 低いCF: 過剰コンプレッション「ラウドネスウォー」
      - Librosaでの近似: 
    p.max(np.abs(y)) / (np.sqrt(np.mean(y**2)) + eps)
    - **Loudness Range (LRA, EBU R128)**:
      - 既存の dynamic_range (95pct - 5pct in dB) で代替可能
      - より精密には: Short-term loudness (3秒窓) の分布の10〜95パーセンタイル差
    - **Peak-to-Short-term Loudness Ratio (PSR)**:
      - PSR = True Peak Level - Short-term Loudness (Max)
      - 業界標準の「パンチ感」指標
    - **参照**: EBU R128 / AES Standard
  </api>
  <api id="VOCAL_PRESENCE">
    <title>Vocal Presence とDemucsステム特徴量</title>
    - **vocal_energy_ratio (既存SNR後処理と同方式)**:
      \text{vocal\_energy\_ratio} = \frac{E_{vocals} + \epsilon}{\sum_{k \neq mix} E_k + 2\epsilon}
      - 現行パイプラインのSNR後処理（Post-Bind Overwrite）と完全同一パターンで実装可能
    - **追加可能なステム別特徴量**:
      - drums_energy_ratio: ドラム支配率 (percussive domination)
      - ass_energy_ratio: ベース支配率
      - ocal_zcr: ボーカルステムのZCR → 子音多いほど高い → 言葉量の代理
      - ocal_hnr: ボーカルステムのHNR → 音声品質・澄んだ歌声の指標
      - ocal_spectral_centroid: ボーカルの明るさ（音域の高さの代理）
    - **Demucs ONNX 実機実装後に取得可能な追加指標**:
      - SDR/SIR/SAR: mir_eval.separation.bss_eval_sources() で計算 (要ground-truth)
      - Pitch confidence: librosa.pyin(vocal_y, fmin, fmax) の voiced_flag割合
    - **参照**: https://github.com/facebookresearch/demucs
  </api>
  <api id="SPECTRAL_CENTROID_DELTA">
    <title>スペクトル重心の時間変化 (Δ Centroid)</title>
    - **式**:
      \Delta C_t = C_{t+1} - C_t
      - より安定した推定には回帰窓版: librosa.feature.delta(centroid_trajectory)
    - **統計量として保存**:
      - spectral_centroid_delta_mean: Δの時間平均 → 全体的な音色変化傾向
      - spectral_centroid_delta_std: Δの標準偏差 → 音色変化の激しさ
    - **物理的意味**: 音の明るさが時間的にどう変化するか → ビルドアップ・ドロップの強度指標
  </api>
  <api id="REPLAY_SCORE">
    <title>Replayability指標の学術的根拠 (Bertin-Mahieux 2017)</title>
    - **参照論文**: Hanna, P. (2017) "Considering Durations and Replays to Improve Music Recommender Systems" arXiv:1711.05237
    - **論文の内容**: 再生時間と再生回数(リプレイ)を暗黙的フィードバックとして利用し推薦精度を向上させる手法
      - 短時間で停止(スキップ) → 負のフィードバック
      - 複数回リプレイ・長時間聴取 → 正のフィードバック
      - MAP@k で推薦品質を評価
    - **Time Decay Collaborative Filtering との接続**:
      \text{Weight}(t) = e^{-\lambda \Delta t}
      - Δt: 現在からの経過日数、λ: 減衰定数
      - より単純な近似(Bertin-Mahieux簡易版):
        \text{Replay Score} = \frac{\text{plays}}{\text{days\_since\_first\_play} + 1}
    - **Repeat Density の学術的根拠**:
      - Time-decay collaborative filtering の枠重関数として論文化済み
        \text{Repeat Density} = \frac{\text{plays}}{\text{active\_days}}
      - 短期集中型(高密度) vs 長期散発型(低密度)を区別する
    - **Love Persistence の根拠**: Spotify Analytics系の実務知見
      - 「recency × engagement」複合指標 (論文ではなく業界実装)
      - Love Persistence = now - love_date (秒→日変換)
    - **実装方針**: FLACタグから FMPS_PLAYCOUNT, FMPS_LAST_PLAYED, FMPS_RATING, PLAY_STARTED 等を読む
      → Analyzeスキーマ実装時に nalyzer スキーマのテーブルとして実装
  </api>
  <api id="GROOVE_QUALIFY">
    <title>Groove Qualify の実装根拠 (Essentia RhythmExtractor2013)</title>
    - **重要な訂正**: Essentiaに DrumExtractor は存在しない
      - 正しくは RhythmExtractor2013 + カスタムpost-processing でGroove指標を構築
    - **Essentia RhythmExtractor2013 API**:
      `python
      import essentia.standard as es
      rhythm = es.RhythmExtractor2013(method="multifeature")
      bpm, beats, beats_confidence, estimates, bpm_intervals = rhythm(audio)
      `
      出力:
      - pm: テンポ推定値
      - eats (=ticks): ビート位置(秒)
      - eats_confidence: 検出信頼度
      - pm_intervals: ビート間隔配列
    - **Groove Qualify の計算フロー**:
      1. eats → 偶数/奇数インデックスに分割 → d1(長め), d2(短め)
      2. Swing Ratio: SR = mean(d1) / mean(d2)
      3. Offbeat Ratio: onsetがbeat間(offbeat位置)に発生する割合
      4. Syncopation Index: Librosaのonset位置とEssentiaのbeat gridのズレから算出
      5. 3段階分類:
         - straight: SR < 1.15
         - swing: 1.15 ≤ SR < 1.7
         - heavy_swing: SR ≥ 1.7
    - **Danceabilityとの関係**: EssentiaのDFA(Detrended Fluctuation Analysis)ベース指標とは別概念
    - **参照**: https://essentia.upf.edu/reference/std_RhythmExtractor2013.html
  </api>
  <api id="FIXED_SEQ_FRAMES">
    <title>固定フレーム時系列の統一設計</title>
    - **定数**: FIXED_SEQ_FRAMES = 32 (旧 TONNETZ_N_FRAMES のエイリアス)
    - **補間方式**: 
    p.linspace(0,1,T) → 
    p.linspace(0,1,32) へ 
    p.interp (Tonnetzと同一)
    - **対象時系列**:
      | seq名 | 次元 | 物理的意味 |
      |-------|------|-----------|
      | centroid_seq | 32 | Spectral Centroid軌跡 (音色の明るさの時間変化) |
      | 
    ms_seq | 32 | RMS軌跡 (音圧の時間変化 = Dynamics) |
      | chroma_entropy_seq | 32 | フレームごとのShannon Entropy (和声複雑度の時間変化) |
      | centroid_delta_seq | 32 | Δ Centroid軌跡 (音色変化速度) |
      | 	onnetz | 192 | 既存 (6軸×32フレーム, frame-major) |
    - **格納先**: PostgreSQL JSONB のみ (FLACタグには入れない)
    - **サマリー統計**: mean/std のみをFLACタグにも書く
    - **共存設計**: 既存の spectral_centroid_mean/spectral_centroid_sd (全体統計) と centroid_seq (時系列) を両方保持
  </api>
  <api id="ONNX_MULTIPROCESS_SAFETY">
    <title>ONNX Runtime Multiprocessing and DirectML Safety</title>
    - **概要**:
      - DirectML Execution Provider (`DmlExecutionProvider`) は、同一推論セッションへの並行 `Run()` 呼び出しに対応しておらず、マルチスレッド並列呼び出しでクラッシュ・デバイスロストを起こす。
      - Pythonの `multiprocessing` によるプロセス分離並列化は可能であるが、GPUコンテキストを共有する際のセグフォを回避するため、必ず `multiprocessing.set_start_method('spawn')` を適用する必要がある。
    - **リソース管理とメモリ**:
      - 各プロセスが個別の推論セッションとモデルデータ（EffNetなど）をロードするため、並列数が増えるにつれてRAMとVRAMの消費量がプロセス数倍に急増する（VRAM-OOMの危険）。
      - そのため、VRAM容量の制約下では、並列プロセス数を厳しく制限（`get_segment_workers()` 等で制御）する、あるいはCPU推論（`CPUExecutionProvider`）へフォールバックして並列プロセス数を引き上げることが推奨される。
      - メモリ最適化（`enable_mem_pattern = False` / `enable_cpu_mem_arena = False`）は、DirectMLを介したマルチプロセス実行時の安定動作に寄与する。
    
    <ref>参照URL: https://onnxruntime.ai/docs/performance/tune-performance.html, https://onnxruntime.ai/docs/execution-providers/DirectML-ExecutionProvider.html</ref>
  </api>
  <api id="LIBROSA_MULTICHANNEL_DIMENSION">
    <title>Librosa Multichannel Auto-detection and Stereo Input Shape Bug</title>
    - **概要**:
      - `librosa` (0.9.0以降) は多次元（マルチチャンネル）入力に対応しており、配列形状 `(channels, samples)` の `channels-first` フォーマットを期待する設計になっておりますの。
      - `soundfile.read` 等でデコードした標準的なステレオ波形 `(samples, channels)` (e.g. channels-last `(N, 2)`) をそのまま `librosa` の特徴量抽出関数（例: `librosa.feature.melspectrogram`）に渡すと、`librosa` はこれを `channels = N`, `samples = 2` の `channels-first` 配列と誤認してしまいますわ。
      - この誤認により、`librosa.stft` 内部で極端に短い `length=2` のシグナルに対してデフォルト `n_fft` が計算されるため、`UserWarning: n_fft=512 is too large for input signal of length=2` が発生いたしますの。
    - **対策**:
      - `librosa` に波形を渡す前に、必ず次元数 `y.ndim > 1` をチェックし、チャンネル優先/サンプル優先（`shape[0] == 2` または `shape[-1] == 2`）を判別して平均化（モノラル化）する downmix ガードを噛ませる必要がございますわ。
    
    <ref>参照URL: https://librosa.org/doc/latest/multichannel.html</ref>
  </api>
  <api id="MUTAGEN_FLAC_METADATA_EXTRACT">
    <title>mutagen による FLAC メタデータの全件抽出とトラック個別フィルタリング</title>
    - **概要**:
      - `mutagen` の `FLAC` オブジェクトからメタデータタグを一括抽出する場合、`meta.items()` から辞書ライクに取得可能であり、キーはすべて小文字（case-insensitive 化された VorbisComment キー）の文字列、値は文字列のリスト（複数値を許容）で表現されますわ。
      - 値のリストを JSONB カラム等に適合させるため、要素数が 1 の場合は文字列へと平坦化し、複数ある場合のみリスト構造を維持する「動的平坦化写像（Dynamic Flattening Mapping）」が有用ですの。
    - **トラック個別フィルタリング**:
      - `CUE_TRACK_XX_...` （例：`cue_track01_event`）といったトラック個別タグが VorbisComment に混在している場合、他のトラックを処理する際にそれらが混入するのを防ぐため、`^(?:cue_)?track_?(\d+)_(.+)$` のパターンマッチングにより、現在処理中のトラック番号 `XX` に合致するもののみを動的マージし、プレフィックスを剥いで `meta` JSONB の共通キー名（例：`event`, `composer`）へマッピングすることで、シングルトラック時とマルチトラック時のスキーマ統一を実現しますわ。
    
    <ref>参照URL: https://mutagen.readthedocs.io/en/latest/user/flac.html, https://xiph.org/vorbis/doc/v-comment.html</ref>
  </api>
  <api id="ESSENTIA_DISCOGS_CLASSIFICATION">
    <title>Essentia Discogs分類モデルとMAEST統合モデルの特性</title>
    - **概要**:
      - EssentiaはDiscogsメタデータを利用した音楽分類モデルを複数公開しており、EffNet-Discogs下流タスクモデルとMAEST統合モデルの2種類に分類されますわ。
    - **EffNet-Discogs下流タスクモデル**:
      - `genre_discogs400-discogs-effnet-1.onnx` や `danceability-discogs-effnet-1.onnx` 等。
      - バックボーン（`discogs-effnet-bs64-1`）から抽出した音楽埋め込み（embeddings: 128次元）を入力として受け取ります。
      - 現行の「メルパッチ抽出 -> EffNet -> 分類器」の2段階推論フローにそのまま適合するため、ファイルを設置するだけで追加改修なしに動作します。
    - **MAEST統合モデル**:
      - `discogs-maest-30s-pw-519l-2.onnx` 等。
      - バックボーンを介さず、16kHzモノラルオーディオ波形 `(batch_size, num_samples)` を直接入力とする独立推論モデルです。
      - 現行の埋め込みを入力とするループに直接噛ませると入力不一致（Shapeミスマッチ）でエラーとなるため、波形入力を分岐して受け渡す推論パス（分岐器）が別途必要になります。
    - **FLACタグ肥大化対策**:
      - 400スタイルや519スタイルなどの多クラスモデルは、全予測確率をFLACタグに書き込むと数万バイト単位でファイルが肥大化しプレーヤーの再生不全を引き起こすため、タグ側には「確率が0.1以上の項目」または「上位5件のスタイル名と確率」などの情報のみに書き込みを制限し、データベース側の `predictions` カラム（JSONB）には生の全結果を保存する分岐設計が求められます。
    - **参照**: https://essentia.upf.edu/models.html, https://github.com/MTG/essentia-onnx
  </api>
  <api id="STEM_TEMPOBEAT_PREWARMING">
    <title>ステムリズム特徴量抽出と並列キャッシュ整合</title>
    - **概要**:
      - ドラム（`drums`）およびベース（`bass`）の分離ステムに対して `tempobeat`（BPMおよびビート位置）および `tempogram` を抽出する際、スレッド並列（ThreadPoolExecutor）処理下での GIL および `LIBROSA_LOCK` (RLock) による競合ブロッキングを回避するため、前段の事前キャッシュ（Pre-warming）層に対象ステムのリズム特徴量を登録する必要がある。
    - **技術的課題と解決策**:
      - 旧設計では、 `AudioContext.tempobeat` は `source != "mix"` の場合に一律でダミー値 `(0.0, np.array([], dtype=int))` を返していた。これはステムでの重いビートトラッキングを回避するための設計であったが、ドラムやベースのリズム特徴量（GrooveやSwing、Beat Regularity等）を正確に分析したいという要求に応えるため、`self.source in ("mix", "drums", "bass")` に対して本物の `librosa.beat.beat_track` を計算するように制限を緩和する。
      - `mix` 以外のステムに対して `librosa_extractor.run(ctx)` がスレッド並列で呼び出されるため、事前キャッシュがない状態で `tempobeat` 等がオンデマンドで評価されると、スレッド内で重い `librosa` 処理が走り、 `LIBROSA_LOCK` によるブロッキングが多発して並列効率が極端に低下する。
      - したがって、直列フェーズである Pre-warming において、`drums` および `bass` ステムに対して `tempobeat`, `onset_env`, `tempogram` の3要素を強制評価しキャッシュを温めておく。
    - **圏論的・代数的整合性**:
      - `FeatureExtractor[T]`（環境 $C$ = `AudioContext` から値 $T$ への射 $C \to T$）は、Applicative 関手（Applicative Functor）としての性質（Identity, Homomorphism, Interchange, Composition）を完全に満たす。
      - 特定のステム（`vocals`, `other`）で `tempobeat` がダミー値を返す挙動は、定義域のコンテキストによる条件付き射（Conditional Morphism）と見なせる。これは同じ `AudioContext` オブジェクトに対して常に同一の観測結果を返す（参照透過性）ため、Applicative の Product 合成 $\prod FeatureExtractor$ およびドメインの構造的整合性を破綻させない。
      - 計算資源とメモリ効率を最適化するための遅延評価プロパティキャッシュ（CSE）は、射の共通因数分解（Common Factorization）に相当し、圏論的図式の可換性を厳密に保持する。
    - **参照**: https://librosa.org/doc/latest/generated/librosa.beat.beat_track.html
  </api>
  <api id="TEMPO_SEQ_EXTRACTION">
    <title>Tempogram からのテンポ時系列（TempoSeq）抽出と32フレームリサンプリング</title>
    - **概要**:
      - `librosa.feature.tempogram` によって得られる2次元テンポグラム `(n_bins, t_frames)` から、時間変化に伴うローカルテンポ（BPM）の時系列（TempoSeq）を抽出する。
      - 各時間フレーム $t$ において最も強度の高い（支配的な）テンポビンのインデックスを `np.argmax(tempogram, axis=0)` によって特定する。
      - `librosa.tempo_frequencies(n_bins, sr=sr, hop_length=hop_length)` を用いて各ビンに対応する物理的なBPM値を取得し、支配的テンポインデックスをマッピングして、時間ごとのテンポ（BPM）時系列を構成する。
    - **補間と固定長化**:
      - 抽出されたテンポ時系列を `_resample_to_fixed_frames`（`np.linspace` + `np.interp`）により、固定フレーム数（`FIXED_SEQ_FRAMES=32`）へ一次元補間・リサンプリングする。
      - これにより、楽曲の各セグメントにおける時間発展テンポ軌跡を一定次元のベクトルとして安全にデータベース（JSONB）へ格納することが可能になる。
      - `mix` のみならず、リズム骨格を形成する `drums` や `bass` のステムに対してもこのテンポ時系列を測定し、ステム間でのテンポ揺れの相関やリズムダイナミクスの分析を圏論的整合性を保ったまま実現する。
    
    <ref>参照URL: https://librosa.org/doc/latest/generated/librosa.tempo_frequencies.html</ref>
  </api>
  <api id="CHORD_SEQUENCE_ESTIMATION">
    <title>クロマテンプレートマッチングによるコード進行系列 (Chord Sequence) の抽出</title>
    - **概要**: 
      - `chroma_cqt` 等から得られる12次元のクロマベクトルに対し、あらかじめ定義されたコードテンプレート（C, Cm, D, Dm 等の各音構成要素）とのピアソン相関係数を算出し、各時間フレームにおいて最も類似度の高いコードを特定する。
      - 音源の調性や和声的な流れを文字列の配列（例: `["C", "Am", "F", "G"]`）としてシーケンス化し、類似進行の検索や構成分析に活用する。
    - **テンプレート構成**: 
      - メジャーコード: `[1, 0, 0, 0, 4, 0, 0, 7, 0, 0, 0, 0]` を主音位置にロール。
      - マイナーコード: `[1, 0, 0, 3, 0, 0, 0, 7, 0, 0, 0, 0]` を主音位置にロール。
    - **参照**: Müller, M. (2015) "Fundamentals of Music Processing" Chapter 5 (Chord Recognition)
  </api>
  <api id="BEAT_SYNCHRONOUS_ANALYSIS">
    <title>ビート同期特徴抽出 (Beat-Synchronous Feature Extraction)</title>
    - **概要**: 
      - 短時間フーリエ変換 (STFT) やクロマグラム等のフレーム単位 (通常 ~23ms) の時系列データを、ビート検出 (Beat Tracking) によって得られた拍のタイミング（ビート境界）に基づいて時間平均（同期化）する。
      - `librosa.util.sync` を用いることで、演奏テンポに依存する「拍（Beat）」のグリッドに時間軸が射影され、1曲の長さが「時間（秒）」ではなく「拍数」で表されるため、長さの異なる音源間での和声構造・音量変化の比較が極めて容易になる。
    - **データサイズ削減効果**: 
      - 4分間の曲で約1万フレームある詳細データが、ビート同期により約300〜400要素（ビート数分）に圧縮され、JSONBのカラムサイズ肥大化を抑えつつ、音楽的解像度の高いコード進行情報（拍ごとのChroma）等を保存可能になる。
    - **参照**: https://librosa.org/doc/latest/generated/librosa.util.sync.html
  </api>
  <api id="VOCAL_F0_ESTIMATION">
    <title>ボーカル基本周波数 (F0) および歌唱特性の抽出</title>
    - **概要**: 
      - 波形分離された `vocals` ステムに対してピッチ検出アルゴリズム `librosa.yin` または `librosa.pyin` (Probabilistic YIN) を適用し、ボーカルの基本周波数 $F_0$（Hz または MIDI Note 番号）の軌跡と発声確率（Voicing Probability）を抽出する。
    - **抽出指標**: 
      - 平均ボーカルピッチ、音域（最高音・最低音）、ビブラート強度（$F_0$ 軌跡の局所周波数変動幅）。
      - ボーカル存在率（Voicing Ratio）: 全体時間に対する発声（Voiced）フレームの割合を算出し、歌唱セクションの密度（と言葉量）を定量化。
    - **参照**: https://librosa.org/doc/latest/generated/librosa.pyin.html
  </api>
  <api id="JSONB_SCHEMA_CLEANUP">
    <title>ステム指向のJSONB特徴量スキーマ最適化 (Dynamic Schema Restriction)</title>
    - **概要**: 
      - 分離ステム（vocals, drums, bass 等）はその役割に応じた音響的性質しか持たないため、全ステムに同一の `LibrosaFeatures` （キーセット）を一律適用すると、無関係なダミー値（例: vocalsにおけるテンポグラムやキー、drumsにおけるChromaなど）が大量に出力され JSONB が肥大化する。
      - ステムのソースキー（mix, vocals, drums...）に応じて、出力する JSON 辞書キーを動的にフィルタリング（Schema-clean化）する。
      - 時系列シーケンスを、類似検索等に即時利用可能な「固定長音楽シーケンス（長さ32等の `summary_sequences`）」と、可視化やより深い分析用の「可変長詳細シーケンス（長さ数千の `detailed_sequences`）」に階層分離し、用途に応じた選択的永続化（Selective Persistence）を行うことで、DBクエリおよびインデックス効率を最大化する。
  </api>
  <api id="ZCR_SCALAR_AND_SEQUENCE">
    <title>ZCRのスカラー統計量と時系列シーケンス (ZcrFeatures) の両面抽出</title>
    - **概要**: 
      - Zero Crossing Rate (ZCR) は、信号がゼロレベルを横切る頻度を示し、音響的にはノイズ感（Noise-like signal vs Tonal signal）や打楽器アタック、ボーカルの有声・無音（Voiced/Unvoiced）判定に用いられる。
      - スカラー値としての平均値（`zcr_mean`）と標準偏差（`zcr_std`）は曲全体のノイズ成分や明るさの静的指標として機能し、32次元の時系列シーケンス（`zcr_seq`）は曲の進行に伴う「静と動」のコントラストやセクション移行時の音響変化を動的・構造的に捕捉する。
    - **計算方法**: 
      - `librosa.feature.zero_crossing_rate` によってフレーム単位の ZCR を算出。
      - 全時間軸の平均（mean）と標準偏差（std）を `scalars` にバインド。
      - 時間軸に沿って 32 次元固定長にダウンサンプリング（`_resample_to_fixed_frames`）し、`sequences` にバインド。
    - **参照**: https://librosa.org/doc/latest/generated/librosa.feature.zero_crossing_rate.html
  </api>
  <api id="PYTHON_COLOR_LOGGING">
    <title>Windows/Linux両対応のPythonログカラー出力</title>
    - **概要**:
      - Pythonの標準 `logging` モジュールにおいて、WindowsおよびLinuxの双方でANSIエスケープシーケンスを用いたカラーログ出力を実現する手法。
      - LinuxターミナルやモダンなWindows環境（Windows Terminal, PowerShell 7+）は標準でANSIエスケープシーケンスをサポートしているが、レガシーなWindows環境（標準 `cmd.exe` 等）ではエスケープ文字がそのまま表示されてしまうため対策が必要。
    - **実装アプローチ**:
      - **ライブラリ依存アプローチ (Rich / colorlog / coloredlogs)**:
        - `rich.logging.RichHandler`: 最もモダンで美麗なカラーロギングを提供。自動で環境判定を行い、適切にカラーコードを出力する。
        - `colorlog.ColoredFormatter` もしくは `coloredlogs.install()`: 既存 of `logging` 設定に容易に結合でき、Windows上では `colorama` を介して自動的にANSIエスケープを処理する。
      - **標準ライブラリのみによるアプローチ (Custom Formatter + Windows VT Mode)**:
        - `logging.Formatter` を継承したカスタムクラスを作成し、`record.levelname` に対応するANSIエスケープコード（例: `\033[33m`）を動的に付与する。
        - Windows環境でエスケープコードを正常にレンダリングさせるため、`ctypes` を使用して `kernel32.SetConsoleMode` を呼び出し、仮想端末処理 (`ENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004`) を有効化する。
    - **注意点**:
      - **シェル起動時の挙動（PowerShell 7 .ps1 / Linux sh）**:
        - **Windows (PowerShell 7)**: `pwsh.exe` や Windows Terminal 等のホスト環境は標準で ANSI コードに対応しておりますが、Pythonプロセス側が「出力先がANSIシーケンスを受理可能か」を自動検出できないことがございますわ。そのため、何も対策しないと生の `←[33m` のような制御文字が露出することがあるため、Windows環境では `colorama.just_fix_windows_console()` を呼ぶか、`ctypes` で明示的に `SetConsoleMode` を呼んで VT (Virtual Terminal) 処理を有効化するのが極めて安全ですわ。
        - **Linux (sh)**: TTY (端末) に接続されている限り、特別な初期化コードなしでANSIカラーが正しく解釈されますの。
        - **共通 (パイプとリダイレクト)**: シェルスクリプト経由で `python script.py | tee output.log` のようにパイプやリダイレクトを使用すると、`sys.stdout.isatty()` が `False` になり、カラーコードが出力に残ってしまいますわ。ロギングを実装する際は `sys.stdout.isatty()` で判定し、TTYが `False` の場合は色コードの付与をスキップするロジックが必要になりますわ。
      - ファイル出力ハンドラ（`FileHandler`）等にも同一のカラーフォーマッタを適用すると、ログファイル内に不可視の制御文字（`\033[33m` 等）が混入してパースの妨げになるため、コンソール（`StreamHandler`）とファイル出力用でフォーマッタを分離する必要がある。
    - **参照**:
      - [Python logging HOWTO - Standard Library](https://docs.python.org/3/howto/logging.html)
      - [Rich Logging - Rich Documentation](https://rich.readthedocs.io/en/latest/logging.html)
      - [ctypes — A foreign function library for Python](https://docs.python.org/3/library/ctypes.html)
  </api>
  <api id="POWERSHELL_SKIP_BY_LOG">
    <title>PowerShellバッチにおけるログ完了検知とスキップ処理 (Idempotent Execution via Logs)</title>
    - **概要**: 
      - 大規模なバッチ処理において、クラッシュや中途終了後の再開効率を最大化するため、各サブディレクトリ（処理単位）に対応するログファイル内から「完了を示す特定の識別トークン」をスキャンし、既に完了していると判定された処理を冪等（Idempotent）にスキップする手法。
      - プロセス自体のExitCodeだけではなく、実ログファイルの末尾や特定パターンを検証することで、中途半端な失敗状態のまま完了と誤認されるのを防ぐ。
    - **実装アプローチ**:
      - `Get-Content -Path $logFilePath -Encoding utf8 -Raw` を使用してファイル全体を一括してメモリ上に読み込み、`-like "*完了識別メッセージ*"` や `-match` などの演算子で検証する。
      - スキップ機能の切り替えを容易にするため、`[switch]$Skip` 等のパラメータを設けてデフォルトを無効にし、ユーザーが必要に応じて明示的に有効化できるように設計する。
    - **参照**:
      - [Get-Content - Microsoft Learn](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.management/get-content)
  </api>
  <api id="SHARED_MEMORY_WIN_MEMLEAK">
    <title>WindowsにおけるSharedMemoryのハンドル保持に起因するメモリリークとI/Oエラー</title>
    - **概要**:
      - Windows環境下において、Pythonの `multiprocessing.shared_memory.SharedMemory` はシステムページングファイル (`Pagefile`) をバッキングストアとする `Memory-mapped file` を作成します。
      - Windowsの仕様上、作成された SharedMemory はハンドルが開いたまま（参照されているプロセスが存在する状態）になっている限り、Consumer 側で `unlink()` を呼び出しても物理メモリおよびコミットチャージ領域が解放されません。
      - 大規模なバッチループなどでハンドルをクローズ（`close()`）せずに保持し続けると、RAM使用量が数十GB（例: 56GB/64GB）に達してシステムリソースが完全に枯渇します。
      - この極限メモリ枯渇状態下において、ファイル書き込み（`numpy.save` や `array.tofile` 等）を実行すると、OSが内部I/Oバッファリング用のメモリリソースを確保できず、エラー `ERROR_NO_SYSTEM_RESOURCES` (1450) や `ERROR_NOT_ENOUGH_MEMORY` (8) が発生し、結果として `OSError: [X] requested and 0 written` という奇妙な「0バイト書き込みエラー」が引き起こされます。
    - **対策**:
      - 参照維持のために一時的にハンドルをプールする場合でも、リングバッファやFIFOキャッシュを用いて、一定件数（例: 最大 64 トラック分）に達した古いハンドルから順次 `close()` を呼んで参照を捨て、自動解放を促す必要があります。
    - **参照**:
      - [multiprocessing.shared_memory — Shared memory for direct access across processes](https://docs.python.org/3/library/multiprocessing.shared_memory.html)
  </api>
  <api id="FUNCTIONAL_SHARED_MEMORY_ARCH">
    <title>Functional Programming and Shared Memory Architecture in Category Theory</title>
    - **概要**: 関数型プログラミングにおいて共有メモリ（Shared Memory）は強力な「可変状態（Mutable State）」であり、参照透過性（Referential Transparency）を破壊し、並行処理における非決定性（Nondeterminism）を生む原因となる。圏論的アーキテクチャでは、これを純粋な関数（Morphism）と副作用（Effect）に分離して扱う必要がある。
    - **圏論的モデル化とState/IO Monad**: 
      - 共有メモリに対するアタッチおよび読み書きは、純粋な `A -> B` の関数合成ではなく、`A -> IO[B]` または `A -> State[S, B]` のようにモナド（Monad）で包み込むことで、副作用の境界を明示的に隔離する。
    - **設計の修正案 (Implementation Proposals)**:
      1. **Message Passing over Shared Memory (Actor Model パターン)**: 共有メモリをミュータブルなグローバル状態としてではなく、所有権（Ownership）をプロセス間で移譲する「ゼロコピー・チャネル」上のメッセージとして扱う。GoからPythonへ共有メモリ名（ポインタ）を渡し、書き込み完了後に所有権を移行する。
      2. **WORM (Write-Once, Read-Many) / 線形型的運用**: Demucsが共有メモリに波形を書き込んだ瞬間から、そのメモリ領域を Immutable（不変）として凍結する。Librosa 等の後続プロセスはこれを Read-Only で参照することで、データ競合を理論上排除し参照透過性を保つ。
  </api>
</knowledge>

<api id="PowerShell_GetItem_Brackets">
### Get-Item Wildcard Parsing
Context: Get-Item $path fails on valid paths containing brackets [].
Gotchas: Brackets are interpreted as wildcards. Use -LiteralPath to prevent $null returns and downstream 0-byte allocations.
</api>

<api id="WinError5_Mmap">
### Windows mmap WinError 5
Context: [WinError 5] Access Denied when calling mmap in Python.
Gotchas: Occurs if the requested mmap size exceeds the size of the existing Windows File Mapping Object allocated by the creator (e.g., Go).
</api>
<api id="Mutagen_FLAC_Tags_CUE_TRACK">
<title>FLAC tags prefix for CUE tracks</title>
<context>FLAC files with Embedded CUE sheets (via Mutagen) historically used "cue_trackXX_" (with underscore) as a prefix for per-track features, not "CUETRACK00_".</context>
<finding>We assumed tags would lack underscores based on memory, but actual data in existing FLACs contained tags like "cue_track07_essentia...". The correct parsing regex should accommodate "^(?:cue_?)?track_?(\d+)_(.+)$" to correctly strip the prefix and parse both legacy "cue_track" and newer track features. Write operations should stick to "CUE_TRACK{num:02d}_" for backward compatibility.</finding>
<gotchas>Applying Demucs and Essentia tags correctly requires passing the "prefix" argument recursively during their "to_flac_tags" generation.</gotchas>
</api>


<api id="psycopg2_upsert_postgresql">
<title>PostgreSQL UPSERT with psycopg2</title>
Context/Finding/Source/Gotchas:
- Use "INSERT ... ON CONFLICT (id) DO UPDATE SET ..." syntax for UPSERT.
- Use "EXCLUDED.column_name" to reference the values proposed for insertion.
- Pass values as parameters to avoid SQL injection.
</api>

<api id="PURE_GO_SQLITE_WINDOWS">
<title>WindowsにおけるCGOフリーなSQLiteの利用 (modernc.org/sqlite)</title>
Context/Finding/Source/Gotchas:
- `github.com/mattn/go-sqlite3` は CGO（GCC等のCコンパイラ）を要求する。GCC の存在しない Windows 環境下で CGO_ENABLED=0 を指定してビルドすると、エラーを出さずにコンパイルが通りバイナリが生成されるが、実行時のDB初期化等で `go-sqlite3 requires cgo to work. This is a stub` という致命的なスタブクラッシュを発生させる。
- GCC不要でコンパイル・動作可能な完全 Pure Go の SQLite 実装である `modernc.org/sqlite` を代わりに使用することで、いかなる環境下でもビルドと安定稼働を保証できる。
- ドライバ名は `sqlite3` ではなく `sqlite` になる点に注意（`sql.Open("sqlite", dbPath)` と指定する）。
</api>

<api id="GO_SUBPROCESS_VENV_PYTHON">
<title>Goの子プロセス起動におけるPython仮想環境のパス解決</title>
Context/Finding/Source/Gotchas:
- Go から単に `exec.Command("python.exe", ...)` を実行すると、環境変数 PATH の優先順位によりシステムのグローバルな Python や Windows Store の Python 実行ファイルが起動される。
- これにより、プロジェクトの仮想環境 `.venv` にインストールされている依存ライブラリ（`librosa` や `psycopg2` 等）がインポートできず `ModuleNotFoundError` を発生させる。
- 実行バイナリのパス（`os.Executable()`）から動的に相対パスを探索し、`parentDir/.venv/Scripts/python.exe` (Windows) または `parentDir/.venv/bin/python` (Unix系) が存在すればそれを優先的に `exec.Command` の第1引数として渡す動的パス解決の実装が不可欠である。
</api>

<api id="DEMUCS_ONNX_OFFLINE_MODE">
<title>HuggingFace Hubのオフラインモード環境変数によるモデルダウンロードエラーとローカルキャッシュ直接解決</title>
Context/Finding/Source/Gotchas:
- 環境変数 `HF_HUB_OFFLINE = 1` が設定されていると、Hugging Face Hub API クライアントはオフラインモードで動作し、リモート接続を行わない.
- ローカルキャッシュ（huggingface_hubのフォルダ構造）にモデルファイルが存在している場合でも、`huggingface_hub` ライブラリの仕様上、明示的に `local_files_only=True` を指定して呼び出さない限り、リモートに HEAD リクエスト等を送ろうとしてしまい、結果的にオフラインエラー `OfflineModeIsEnabled` または `LocalEntryNotFoundError` がスローされる.
- これを完全に回避するため、`models.py` 内で `glob` を用いてローカルのキャッシュ先（`cache_dir/models--StemSplitio--htdemucs-6s-onnx/snapshots/*/htdemucs_6s_fp16weights.onnx`）を直接探索するロジックを導入。キャッシュファイルが物理的に見つかった場合は `huggingface_hub` API の呼び出しを完全にバイパスして直接ファイルパスを渡すことで、オフラインモード（`HF_HUB_OFFLINE=1`）下でも 100% 安定して瞬時にモデルがロードされるようになった。
</api>

<api id="GIT_FILTER_REPO_CLEANUP">
  <title>git-filter-repoによるGit履歴からの巨大ファイル削除と.git肥大化抑制</title>
  <context>リポジトリのコミット履歴に Demucs ONNX モデル関連の巨大な blob (130MB) や、Go のビルド生成物である orchestrator.exe (21MB) が混入し、GitHub への push が制限されるなど .git ディレクトリが肥大化する問題が発生しましたわ。</context>
  <finding>git-filter-repo は、git filter-branch の代替として公式に推奨されている、Gitリポジトリの履歴を高速かつクリーンに書き換えるPython製ツールです。以下のコマンドで履歴から特定の巨大ファイルを完全に除外できますの。
  1. pip install git-filter-repo 等によるインストール。
  2. 履歴から削除するファイルを指定して実行：
     git filter-repo --invert-paths --path demucs/models--StemSplitio--htdemucs-6s-onnx/blobs/7ce55792e2231c93fbf92de95f5fd5b3a5e6c89f7db690dfd693e8f1dce56869
     git filter-repo --invert-paths --path orchestrator/orchestrator.exe
  3. 履歴の書き換え後は、.git 内の不要な blob オブジェクトが整理され、容量が劇的に縮小されますわ。</finding>
  <gotchas>履歴書き換えを行うため、ローカルでクローンまたはバックアップを作成した上で実行することが推奨されますわ。また、リモートリポジトリへ同期する際は git push --force が必要になりますが、プロジェクトルールで git push は禁止されているため、履歴書き換え実行のタイミングは旦那様と相談して調整する必要がございますの。</gotchas>
</api>

<api id="postgres_latest_analyzed">
### PostgreSQL raw.library_flac 最新解析レコード取得
- Context: DB正規化の検討および最新の解析レコード (`analyzed_at`) の確認。
- Finding: `raw.library_flac` から `analyzed_at IS NOT NULL` のレコードを `analyzed_at DESC` で取得完了。最新レコード ID は 44627、`analyzed_at` は `2026-07-23 22:20:07.826320+09:00`。
- Source: `postgres://ingester:ingester_8852@db.tigris-tailor.ts.net:5432/db`
- Gotchas: ファイルパス表記やタイトル文字列は文字コード変換・デコーダー依存に注意。
</api>