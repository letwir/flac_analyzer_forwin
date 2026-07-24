# STFT Hann 窓適用に伴う計算結果変更の注意書き追加計画

## 概要
`worker_tensor.py` の `torch.stft` にて `torch.hann_window` を明示指定したことにより、従来発生していたスペクトル漏れ（Spectral Leakage）が解消され、Spectral Flux 等の特徴量算出結果が精度補正された点について [README.md](file:///a:/Users/letwir/repo/flac_analyzer_forwin/README.md) に注意書きを追加・同期します。

## 変更内容
1. `README.md` (日本語 / 英語):
   - STFT 窓関数 Hann 窓適用に伴う計算結果変更に関する `[!NOTE]` アラートの追加。
   - 再解析時の `.\run_batch.ps1 -Force` の案内を明記。
