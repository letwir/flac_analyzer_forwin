# STFT Hann 窓適用に伴う計算結果変更の注意書き追加 Walkthrough

## 変更内容
- **README.md (日本語 / 英語)**:
  - `worker_tensor.py` の STFT 窓関数（Hann 窓）指定によるスペクトル漏れ解消と、過納データに対する精度補正についての注意文（`[!NOTE]` アラート）を追加。
  - 再解析を希望する場合の `run_batch.ps1 -Force` の案内を追加。
- **worker_tensor.py**:
  - `torch.stft` 呼び出し箇所へ `torch.hann_window(1024, device=device)` を明示的に設定。

## 検証
- [README.md](file:///a:/Users/letwir/repo/flac_analyzer_forwin/README.md) の表示レイアウトおよびアライメントを確認。
- [worker_tensor.py](file:///a:/Users/letwir/repo/flac_analyzer_forwin/worker_tensor.py) の構文・動作チェック完了。
