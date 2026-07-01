# 機能拡張と圏論的アーキテクチャの再構築 (Feature Expansion & Categorical Refactoring)

既存のパイプラインに「位相」「非定常性」「ステム間コヒーレンス」等の高度な波形解析パラダイムを導入しつつ、計算の純粋性（Pure Morphisms）を保つためのアーキテクチャ再設計プランです。

## User Review Required

> [!WARNING]
> **Coherence (ステム間相互相関) の導入によるドメイン拡張**
> 既存の分析基盤は $\text{Stem} \to \text{Feature}$ という「単一対象からの射」で構成されていましたが、Coherence の計算には $\text{Stem} \times \text{Stem} \to \text{Feature}$ という**直積 (Categorical Product)** が必要になります。これを無理に単一ワーカー内に押し込むと参照の透明性が破壊される（圏論的破綻）ため、明示的に `StemPair` という対象を定義し、専用の直積射（Cross-spectrum morphism）を割り当てる必要があります。

> [!IMPORTANT]
> **データベーススキーマ (JSONB) の構造変更**
> `StemPair` (例: `bass_drums_coherence`) のようなクロスステムの特徴量をどこに格納するかが課題です。
> `features -> cross_stems -> bass_drums -> {coherence, phase_lag}` のような新しい階層を設けるかご判断ください。

## Open Questions

1. **Pre-cache の保持フォーマット**: `precache.py` が生成する STFT や CQT の複素数行列（位相情報を含む）は巨大です。これらを共有メモリ (Shared Memory) 上に載せて各ワーカーに分配する方針でよろしいでしょうか？
2. **CWT (連続ウェーブレット変換)**: ご指摘の通り `scipy.signal.cwt` は非推奨です。依存関係に `pywt` (PyWavelets) を追加してもよろしいでしょうか？
3. **新規メトリクスの選定**: すべて実装すると抽出時間が大幅に延びる可能性があります。特に優先したい射（例: 瞬時位相、Spectral Flux、Coherenceなど）を絞り込みますか？

## Proposed Changes

計算の純粋性（EffectfulとPureの分離）を徹底するため、ワーカーをライブラリや計算コンテキストごとに分割します。

---

### [Architecture]

#### [NEW] `precache.py` (Memoization Functor)
波形 $X$ から周波数領域表現 $STFT_{\mathbb{C}}(X)$ 等への変換（Functor）を担います。
位相情報を捨てない複素スペクトログラムや、再利用可能なフィルタバンク出力を生成し、不変な（Immutable）キャッシュ対象として後段のワーカーへ提供します。これにより、LibrosaやScipyの間で計算の重複（O(N)の浪費）を防ぎます。

#### [MODIFY] `librosa_worker.py` (Morphism: $STFT \to Feature$)
`precache.py` が生成した周波数領域表現を入力とする射の集合です。
- 既存のMFCC, Chroma, Spectral Flux などを担当。
- 振幅・周波数重心などの「既存の強み」を純粋な関数として計算します。

#### [NEW] `scipy_worker.py` (Morphism: $Audio \to Feature$ / $Stem \times Stem \to Feature$)
生波形および直積対象を入力とする射の集合です。
- **Phase spectrum**: `hilbert` による瞬時位相の抽出
- **Coherence / PSD**: `welch`, `coherence`, `csd` 等を用いた非定常性・位相相関の計算
- **Band-limited envelope**: `sosfiltfilt` を用いた帯域別エンベロープの抽出

---

### [Database Schema Expansion]

#### [MODIFY] `db.py` / `pipeline.py`
JSONB カラム `features` に対して以下の拡張を定義します。
- `features -> stems -> {name} -> sequences -> spectral_flux`
- `features -> stems -> {name} -> sequences -> inst_phase`
- `features -> cross_stems -> {pair_name} -> coherence_peaks`

## Verification Plan

### Automated Tests
- `scipy_worker.py` および `librosa_worker.py` に対し、純粋な NumPy 配列を注入して副作用がないこと（Referential Transparency）をテストします。
- `precache.py` の出力が両方のワーカーで正しく読み取れるか（Isomorphism / 変換の整合性）を検証します。

### Manual Verification
- 旦那様の環境（5950X, 64GB RAM）にて、全ワーカーを並列稼働させた際の OOM 発生有無およびスループットを計測します。
- JSONB に新規追加された特徴量が、期待通りの階層構造で UPSERT されているか確認します。