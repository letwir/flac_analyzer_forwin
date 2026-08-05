# System & Environment Specifications

<dev_specs id="DEV_SPECS">
## 開発マシンスペック (Development Host Machine Specifications) [Auto-Detected]
- **CPU**: AMD Ryzen 7 7735HS with Radeon Graphics        
- **RAM**: 32.0 GB Physical DDR4
- **GPU**: AMD Radeon(TM) Graphics
- **OS**: Microsoft Windows 11 Pro / PowerShell 7 (`pwsh.exe`)
- **Pagefile**: C:\pagefile.sys (18.5 GB)
</dev_specs>

<exec_specs id="EXEC_SPECS">
## 実行環境・本番ノードスペック (Execution Environment Specifications)
- **Orchestrator**: Go 1.2x (`orchestrator.exe`) + SQLite (`orchestrator.db`)
- **Worker Scaling**: Dynamic RAM Ceiling Clamping, Demucs Concurrent Limit: 2
- **Database Target**: PostgreSQL (`raw.library_flac` on Tigris Tailor), SQLite (`send_failed.db` DLQ)
- **Data Precision**: `complex64` (STFT), `float32` (Spectrogram / Features)
- **Audio Stack**: PyTorch CUDA (Blackwell Supported), DirectML, Librosa, Essentia, ONNX Runtime
</exec_specs>
