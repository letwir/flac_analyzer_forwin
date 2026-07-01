# Walkthrough: worker_tensor.py の実装

本ドキュメントは、新たな特徴量（Phase, PSD, Spectral Flux等）の導入に伴う `worker_tensor.py` の実装およびインテグレーションの記録ですわ。

## 実装内容 (Implemented Features)

1. **`worker_tensor.py` の純粋な射の実装**:
   - `torch` (PyTorch) を用いた高速なテンソル計算基盤を構築いたしました。
   - `hilbert_envelope_phase`: FFTベースのHilbert変換により、瞬時位相(Phase)とエンベロープを計算。
   - `welch_psd`: `torch.stft` を用いた高速な平均化PSD（Power Spectral Density）の算出。
   - `fft_bandpass_envelope`: 理想帯域フィルタリング後のエンベロープ抽出（Sub-bass 20-60Hz等に適用可能）。
   - `Spectral Flux`: 連続フレーム間のマグニチュード差分の二乗和平方根を抽出。
   - すべて `worker_librosa.py` と同じく、Demucs分離後の共有メモリ (SHM) から Zero-copy (`torch.from_numpy`) で波形をアタッチする仕様としております。

2. **Go オーケストレーター (`orchestrator/main.go`) の接続**:
   - Librosaワーカーの後続プロセスとして、`worker_tensor.py` の起動シーケンスを追加いたしましたわ。
   - 出力された特徴量は `{trackHash}_{baseName}_tensor.json` として `queue` ディレクトリに保存されます。
   - `go build` によるコンパイル成功を確認済みです。

3. **DB取り込み (`ingester.py`) の拡張**:
   - `ingester.py` に `--tensor-json-path` 引数を新設。
   - Tensorワーカーが抽出した位相やPSD情報を、JSONBの `features` カラム（mixおよび各demucsステム階層）に対して自動的に `update` してマージする処理を追加いたしました。

## 今後の展望 (Next Steps)
- `functor_precache.py` の実装: Librosa と Tensor で重複している STFT 計算などを前段の `precache` で共有化するか否かの実装検討へと移りますの。