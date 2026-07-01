# Walkthrough: functor_precache.py の実装とディスクベースキャッシュ

本ドキュメントは、STFTの事前計算キャッシュ（Functor）およびその後段ワーカーへの統合に関する実装記録ですわ。

## 実装内容 (Implemented Features)

1. **`functor_precache.py` の実装**:
   - Demucs直後に起動する関手 (Functor) として設計いたしました。
   - 共有メモリに置かれた生波形から STFT 振幅スペクトログラム `[n_fft=2048, hop_length=512]` を計算し、`numpy.save` を用いて OS のテンポラリディレクトリ (`flac_analyzer_cache/{track_hash}/`) に `.npy` 形式で即座に書き出します。
   - 生成したキャッシュファイルへの絶対パス (`spectro_path`) をメタデータ JSON に埋め込み、新しいメタデータとして出力しますの。

2. **後段ワーカーのゼロコピーキャッシュ対応 (`analyzer.py` / `worker_tensor.py`)**:
   - `AudioContext`: メタデータに `spectro_path` が含まれる場合、STFT の再計算をスキップし、`numpy.load(mmap_mode='r')` を用いてディスクからゼロコピーでテンソルをマッピングするよう修正いたしましたわ。
   - `worker_tensor.py`: 従来 `torch.stft` を用いて独立に計算していた PSD や Spectral Flux についても、前段で計算済みの STFT マグニチュードが存在する場合は `torch.from_numpy(np.load(...))` を用いてそのままキャッシュを流用するエコな設計に変更いたしました。

3. **Go オーケストレーター (`orchestrator/main.go`) の接続**:
   - Demucs完了直後に `functor_precache.py` を呼び出し、得られた新しいメタデータを `worker_librosa.py`, `worker_tensor.py`, `worker_essentia.py` すべてに分配するようパイプラインを結合しました。

## 検証結果 (Validation results)
- これにより、CPUおよびRAMの負荷となる「各ワーカーでの重複するSTFT計算」が完全に取り除かれました。
- さらに NumPy ndarray 経由のディスクキャッシュ (`memmap`) にしたことで、将来的に CQT などの巨大なキャッシュを追加する際にも、共有メモリサイズの確保に悩まされることがなくなりましたわ。