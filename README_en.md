# Flac_Analyzer

🇺🇸 [English version](README_en.md) / 🇯🇵 [日本語版](README.md)

## What is this?

**Flac_Analyzer** is a high-performance system designed to extract acoustic features from FLAC audio files (with full CUE sheet split support), automatically classify music genres and moods via AI, and persist structured feature payloads directly into PostgreSQL.
Optimized for massive audio libraries (50 GB to several TBs) on Windows environments, it completely eliminates Out-Of-Memory (OOM) crashes and database processing bottlenecks.
By leveraging Go parallel job orchestration and Windows Shared Memory (SHM) Write-Once-Read-Many (WORM) IPC transfers, it fully utilizes all CPU logical cores at 100% load while processing audio rapidly and safely.

---

## Requirements

### 1. Operating Environment
- **OS**: Windows 11 / Windows 10 (64-bit)
- **Python**: Python 3.12 or 3.13 (Virtual environment `.venv` strongly recommended)
- **Go**: Go 1.22+ (For building the Go orchestrator. The included `init.bat` or pre-compiled `orchestrator.exe` can also be used directly)
- **PostgreSQL**: PostgreSQL 14+ (For feature persistence)

### 2. PostgreSQL Database Requirements
Please create a database on your PostgreSQL server and execute the included DDL script to initialize schema, triggers, and roles:

```bash
# Log in to PostgreSQL and create database
CREATE DATABASE flac_analyzer_db;

# Run schema initialization script
psql -d flac_analyzer_db -f sql/schema.sql
```

---

## USAGE

### 1. Environment Setup

#### 💡 Standard Environment (RTX 30xx / 40xx, DirectML, CPU)
Create a Python virtual environment and install standard dependencies:

```powershell
# Create and activate virtual environment
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1

# Upgrade pip and install requirements
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

- **For NVIDIA GPU (CUDA 12.x)**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-directml
  pip install onnxruntime-gpu
  pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  ```
- **For AMD / Intel iGPU DirectX 12 (DirectML)**:
  ```powershell
  pip uninstall -y onnxruntime onnxruntime-gpu
  pip install onnxruntime-directml
  ```

#### 🚀 NVIDIA RTX 50xx Series (Blackwell Architecture / CUDA 13.2+)
If you are running Blackwell generation GPUs (GeForce RTX 5070 Ti / 5080 / 5090), use `requirements-blackwell.txt`:

```powershell
pip install -r requirements-blackwell.txt
```

> [!TIP]
> For detailed Blackwell installation steps and verification commands, see [NVIDIA RTX 50xx Installation Guide (docs/install_blackwell_rtx50.md)](docs/install_blackwell_rtx50.md).

---

### 2. Configuration Setup
Copy `config.toml.example` to `config.toml` in the project root and configure PostgreSQL connection string and options:

```toml
[database]
url = "postgres://username:password@hostname:5432/flac_analyzer_db"

[orchestrator]
num_workers = 0  # 0 enables full auto-scaling based on CPU cores & physical RAM
max_ram_ratio = 0.625
demucs_concurrent_limit = 1
shm_allocation_delay_sec = 2
queue_dir = "../queue"
skip_dup_by_hash = true
```

#### ⚙️ Configuration Parameters

| Parameter | Type | Description |
| :--- | :--- | :--- |
| `database.url` | String | PostgreSQL connection URI (`postgres://user:pass@host:port/dbname`). |
| `orchestrator.num_workers` | Integer | Max parallel worker process limit. Setting `0` automatically determines safe worker count based on host physical RAM and CPU cores. |
| `orchestrator.max_ram_ratio` | Float | Maximum system RAM usage ratio (Default: `0.625`). Monitors available RAM in real-time and applies backpressure throttling if exceeded. |
| `orchestrator.demucs_concurrent_limit` | Integer | Semaphore limit for Demucs source separation (`worker_demucs.py`) to prevent GPU overload and ONNX Runtime SegFaults (recommended `1`). |
| `orchestrator.shm_allocation_delay_sec` | Integer | Synchronization delay (sec) during shared memory allocation/deallocation. |
| `orchestrator.queue_dir` | String | Temporary directory path where extraction workers write result JSONs. |
| `orchestrator.skip_dup_by_hash` | Boolean | When `true`, calculates PCM MD5 hash (`--check-hash-only`) and skips Demucs separation and feature extraction **100%** if hash already exists in PostgreSQL. |

#### 🔄 Forced Re-analysis (`-Force`)
Executing `.\run_batch.ps1 -Force` forces re-analysis of previously processed tracks, bypassing existing SQLite `COMPLETED` states and Postgres hash checks.

---

### 3. AI Model Placement
Place necessary ONNX classifier models and mapping JSONs into the `models/` directory (e.g., `discogs-effnet-bs64-1.onnx`).

---

### 4. Execution Steps

#### Step 1: Launch Go Orchestrator
Execute `init.bat` at project root or build and launch in `orchestrator` directory:

```powershell
# Option A: One-tap build and run via init.bat
.\init.bat

# Option B: Manual build and run
cd orchestrator
go build -o orchestrator.exe
.\orchestrator.exe
```

#### Step 2: Submit Batch Directory Scan
Run directory scanner script from a separate PowerShell window. Uses Rust high-speed scanning (`fd.exe` / `rg.exe`) automatically when available.

```powershell
# Standard batch processing (with hash skip enabled)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library"

# Force re-analysis of all files (-Force flag)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library" -Force
```

#### Step 3: Replay Failed DLQ Tasks
Re-transmit and synchronize payloads that failed database insertion and were saved to local Dead Letter Queue (`send_failed.db`):

```powershell
.venv\Scripts\python.exe retry_ingest.py
```

---

## Detailed Overview

**Flac_Analyzer** eliminates memory starvation (OOM) and database bottlenecks through modern engineering architectures:

- **Autonomous Hardware Detection & Dynamic Spec Tagging**: On startup, Go native sysinfo (and Win32 CIM script) auto-detects host physical RAM, CPU, and GPU specs, dynamically tagging `HARDWARE_SPECS.md`.
- **Waveform-based Demucs RAM Estimation & GO/NOGO Gatekeeper**: Pre-calculates required RAM for Demucs based on PCM audio duration. Pauses task dispatching (Gatekeeper Decision) if available RAM is insufficient, eliminating OOM crashes.
- **`tensorSemaphore` & VRAM Cleanup**: Implements `tensorSemaphore` in Go dispatcher and invokes `torch.cuda.empty_cache()` after each PyTorch worker finishes for sequential VRAM cleanup.
- **Go Concurrent Job Dispatching**: Continuously monitors available memory, CPU cores, and `MaxRamRatio` backpressure to dynamically scale worker concurrency.
- **ONNX SegFault Prevention & 3-Tier Parallel Workers**: Enforces `demucs_concurrent_limit = 1` semaphore for Demucs. Once completed, launches `Librosa`, `Tensor`, and `Essentia` workers **in 3 parallel threads** from Freeze-protected shared memory using `sync.WaitGroup`.
- **Windows Shared Memory (SHM) WORM Transfer**: Shared memory is mapped as Read-Only (`PAGE_READONLY`) after Demucs, preventing inter-process memory duplication and fragmentation.
- **float32 Precision Optimization**: Enforces `float32` calculation and hybrid precision protection in Librosa and Scipy feature extractions, preventing Windows pagefile overflow (WinError 1455) even on 25+ minute long tracks.
- **MD5 Waveform Hash Skipping**: Calculates PCM MD5 hash to bypass Demucs and extraction by 100% for existing PostgreSQL tracks.
- **CUE Auto-Parsing & FLAC Fallback**: Auto-parses embedded or sidecar CUE sheets into track segments with automatic fallback for standard FLAC files.
- **Multi-value VorbisComment Tags in JSONB**: Preserves multi-value tags (e.g. `ARTIST`) as JSON arrays (`["...", "..."]`) inside PostgreSQL `meta` JSONB column.
- **Timestamp Preservation**: Preserves and restores exact file creation and modification timestamps after writing VorbisComment tags.

### 📚 Documentation
| Document | Summary |
|:---|:---|
| [State Diagram](docs/state_diagram.md) | Pipeline state transitions & worker flow |
| [ER Diagram & Schema](docs/database_er_diagram.md) | PostgreSQL / SQLite schemas & JSONB specs |
| [SHM / WORM Architecture](docs/shm_architecture.md) | Win32 Shared memory IPC & zero-copy transfer |
| [CPU Parallelism & RAM Guard](docs/cpu_parallelism_and_ram_guard.md) | Worker concurrency, Gatekeeper & RAM defense |
| [CUE Parsing Flow](docs/cue_parsing_flow.md) | CUE sheet parsing & fallback rules |
| [DLQ & Error Recovery](docs/dlq_error_recovery.md) | Dead Letter Queue & zombie task resets |
| [GPU / RAM Fallback](docs/gpu_fallback_and_ram_defense.md) | CUDA/DirectML/Blackwell & VRAM liberation |
| [Blackwell RTX 50xx Setup](docs/install_blackwell_rtx50.md) | Setup guide for NVIDIA RTX 50xx Series (CUDA 13.2) |

---

## State Diagram

Refer to [State Diagram (docs/state_diagram.md)](docs/state_diagram.md) for detailed task execution flows.

---

## ER Diagram & Data Structures

Refer to [ER Diagram & Data Structures (docs/database_er_diagram.md)](docs/database_er_diagram.md) for PostgreSQL/SQLite tables and JSONB specifications.

---

## Windows Shared Memory (SHM) Management & WORM Architecture

Refer to [SHM Architecture (docs/shm_architecture.md)](docs/shm_architecture.md) for Win32 API memory management details.

---

## License

This project is licensed under the [MIT License](LICENSE).

> [!WARNING]
> **Pre-trained Model (ONNX) Licensing Notice**
> Model weights are not bundled in this repository. Verify individual licensing terms (AGPLv3 / CC) for downloaded classifier models (such as Essentia models).
