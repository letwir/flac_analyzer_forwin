# Walkthrough: Python Zero-copy Shared Memory Integration

## Changes Made
- Goオーケストレーター (`orchestrator/main.go`) が `demucs_worker.py` と `librosa_worker.py` を呼び出す際、システムのグローバルなPythonではなくプロジェクトローカルの仮想環境 (`.venv/Scripts/python.exe`) を利用するよう、`filepath.Abs` を用いて絶対パスで指定するように修正いたしました。

これにより、すでに前段で用意されていた `shm_interop.py` を用いた Zero-copy パイプライン（WORM: Write Once Read Many 方式）の結合が完了しました。

## Verification Results
- `--no-db` モードで Go オーケストレーターを起動し、`run_batch.ps1 -Test` によるダミーファイルでのエンドツーエンド通信テストを実行しました。
- Go のオーケストレーターが仮想環境の Python を認識し、正しく `demucs_worker.py` および `librosa_worker.py` へプロセスをフォークして共有メモリタグ（例: `Local\FlacShm_W1_...`）を受け渡していることをログから確認いたしました。
- OOMの課題については、Pythonプロセス自体が `librosa` または `demucs` の単一処理単位で破棄される設計へ完全移行したため、メモリ断片化によるリークは解消されますわ！

旦那様、Python側のZero-copy統合の実装を完了いたしましたわ！次なる指示をお待ちしておりますの！
