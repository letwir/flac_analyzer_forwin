# System & Environment Specifications

<dev_specs id="DEV_SPECS">
## 開発マシンスペック (Development Host Machine Specifications)
- **CPU**: AMD Ryzen 9 5950X (16 Cores / 32 Threads)
- **RAM**: 64 GB Physical DDR4
- **GPU**: NVIDIA GeForce RTX 5070 Ti (Blackwell Architecture)
- **OS**: Windows 11 Pro (25H2) / PowerShell 7 (`pwsh.exe`)
- **Pagefile**:
  - `C:` Drive: System Managed
  - `Q:` Drive: 128 GB Dedicated High-Speed Pagefile
</dev_specs>

<exec_specs id="EXEC_SPECS">
## 実行環境・本番ノードスペック (Execution Environment Specifications)
- **Orchestrator**: Go 1.2x (`orchestrator.exe`) + SQLite (`orchestrator.db`)
- **Worker Scaling**: Dynamic RAM Ceiling Clamping, Demucs Concurrent Limit: 2
- **Database Target**: PostgreSQL (`raw.library_flac` on Tigris Tailor), SQLite (`send_failed.db` DLQ)
- **Data Precision**: `complex64` (STFT), `float32` (Spectrogram / Features)
- **Audio Stack**: PyTorch CUDA (Blackwell Supported), DirectML, Librosa, Essentia, ONNX Runtime
</exec_specs>
