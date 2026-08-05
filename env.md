# Hardware & Execution Environment Specification

## System Overview
- **OS**: Windows 11 (25H2)
- **Shell**: PowerShell 7 (`pwsh.exe`)
- **CPU**: AMD Ryzen 9 5950X (16 Cores / 32 Threads)
- **RAM**: 64 GB DDR4
- **GPU**: NVIDIA GeForce RTX 5070 Ti (Blackwell Architecture)

## Memory & Paging Configuration
- **System Memory (RAM)**: 64 GB Physical RAM
- **Virtual Memory / Pagefile**:
  - `C:` Drive: System Managed Pagefile
  - `Q:` Drive: 128 GB Dedicated High-Speed Pagefile

## Pipeline Infrastructure
- **Orchestrator**: Go 1.2x (`orchestrator.exe`) + SQLite (`orchestrator.db`)
- **Database**: PostgreSQL (`raw.library_flac` on Tigris Tailor)
- **ML / Audio Stack**: PyTorch CUDA (Blackwell Supported), DirectML, Librosa, Essentia, ONNX Runtime
