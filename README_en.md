# Flac_Analyzer (English)

🇯🇵 [日本語版](README.md)

## What is this?

**Flac_Analyzer** is a high-performance audio feature extraction and AI classification system for FLAC music files (including CUE sheet indexing), persisting all results to PostgreSQL.
Optimized for processing large-scale music libraries (from 50GB up to multi-terabyte collections) on Windows without Out-Of-Memory (OOM) crashes or database bottlenecking.
Combines Go-based concurrent job management with Windows Shared Memory (WORM transfer) to maximize CPU utilization across all cores while ensuring memory safety.

---

## Requirements

### 1. System Requirements
- **OS**: Windows 11 / Windows 10 (64-bit)
- **Python**: Python 3.12 or 3.13 (Virtual environment `.venv` recommended)
- **Go**: Go 1.22+ (Required for building the orchestrator; pre-compiled `orchestrator.exe` can also be used)
- **PostgreSQL**: PostgreSQL 14+ (Storage target for analytical data)

### 2. PostgreSQL Setup
Create a target database on your PostgreSQL server and execute the provided DDL script to initialize tables, triggers, and roles:

```bash
# Log in to PostgreSQL and create the database
CREATE DATABASE flac_analyzer_db;

# Run the initialization DDL script
psql -d flac_analyzer_db -f sql/schema.sql
```

---

## USAGE

### 1. Installation
Create a Python virtual environment and install the required dependencies:

```powershell
# Create and activate virtual environment
python.exe -m venv .venv
. .\.venv\Scripts\Activate.ps1

# Install requirements
python.exe -m pip install --upgrade pip
pip install -r requirements.txt
```

#### 💡 GPU Acceleration (NVIDIA CUDA / DirectML) Setup
To accelerate Demucs stem separation and Essentia ONNX inference via GPU:

- **For NVIDIA GPU (CUDA)**:
  ```powershell
  pip uninstall onnxruntime onnxruntime-directml
  pip install onnxruntime-gpu
  pip install torch torchaudio --index-url https://download.pytorch.org/whl/cu124
  ```
- **For DirectX 12 (DirectML) on AMD / Intel iGPU**:
  ```powershell
  pip uninstall onnxruntime onnxruntime-gpu
  pip install onnxruntime-directml
  ```

### 2. Configuration
Configure PostgreSQL connection details, worker parallelism limits, and skip flags in `config.toml`:

```toml
[database]
url = "postgres://username:password@hostname:5432/flac_analyzer_db"

[orchestrator]
num_workers = 4
demucs_concurrent_limit = 1
shm_allocation_delay_sec = 2
queue_dir = "../queue"
skip_dup_by_hash = true
```

#### ⚙️ Configuration Parameter Specifications

| Parameter | Type | Description |
| :--- | :--- | :--- |
| `database.url` | String | PostgreSQL connection URI (`postgres://user:pass@host:port/dbname`). |
| `orchestrator.num_workers` | Integer | Maximum parallel worker processes permitted simultaneously by the dispatcher. |
| `orchestrator.demucs_concurrent_limit` | Integer | Semaphore limit for high-load audio separation tasks (`worker_demucs.py`). |
| `orchestrator.shm_allocation_delay_sec` | Integer | Safety synchronization delay (seconds) when creating/closing shared memory (SHM) regions. |
| `orchestrator.queue_dir` | String | Path to the queue directory where workers write intermediate JSON output payloads. |
| `orchestrator.skip_dup_by_hash` | Boolean | When set to `true`, computes audio MD5 checksums (`--check-hash-only`) before stem separation and **100% bypasses** Demucs and extraction tasks if identical waveform analysis already exists in PostgreSQL. |

#### 🔄 `force: true` (`-Force` Flag) Behavior
When submitting requests via `.\run_batch.ps1 -Force` or passing `"force": true` in the HTTP API body:
1. Existing task status records (`COMPLETED`) in `orchestrator.db` and PostgreSQL hash duplication checks via `skip_dup_by_hash` are **completely bypassed**.
2. Demucs stem separation, feature extraction (Librosa, Tensor, Essentia), and PostgreSQL `JSONB` UPSERT (including auto-archiving to `raw_library_flac_history`) are forcibly re-executed for all targeted files.
3. Useful when recalculating features after windowing/STFT algorithm updates (e.g., Hann window calibration) or AI model upgrades.

### 3. Model Files
Place required ONNX models and label mapping JSON files inside the `models/` directory (e.g., `discogs-effnet-bs64-1.onnx`).

### 4. Running the Pipeline

#### Step 1: Start Go Orchestrator
Launch the Go orchestrator inside the `orchestrator` directory (HTTP API on port `8080`, Prometheus metrics on port `2112`):

```powershell
cd orchestrator
go build -o orchestrator.exe
.\orchestrator.exe
```

#### Step 2: Dispatch Analysis Tasks
In a separate terminal, execute the PowerShell batch scanner script:

```powershell
# Standard batch scan (Duplicate hash skip enabled)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library"

# Force re-analysis of failed or skipped tracks (-Force flag)
.\run_batch.ps1 -Dir "D:\Music\FLAC_Library" -Force
```

#### Step 3: DLQ Retries (Optional)
If PostgreSQL was unreachable during processing, manually retry sending saved payloads from `send_failed.db`:

```powershell
.venv\Scripts\python.exe retry_ingest.py
```

> [!NOTE]
> **Notice Regarding Tensor STFT Window Calibration & Feature Numerical Output**
> `worker_tensor.py` now explicitly applies `torch.hann_window` during STFT processing. This eliminates spectral leakage caused by rectangular windowing, resulting in refined Spectral Flux and Tensor feature outputs. If you wish to re-analyze existing tracks to apply this calibration, run `.\run_batch.ps1 -Force`.

---

## Detailed Overview

**Flac_Analyzer** achieves non-blocking, high-speed execution while eliminating out-of-memory errors through the following technical design:

- **Go-based Concurrent Job Management**: A Go dispatcher monitors system resources (free RAM, CPU load) to dynamically regulate parallel Python worker processes.
- **Windows Shared Memory (WORM Transfer)**: Decoded waveform data and separated stems are transferred using Windows Shared Memory, locked to `PAGE_READONLY` (Write-Once Read-Many) to eliminate inter-process memory duplication and fragmentation.
- **Pre-Hash Duplicate Bypass**: Computes MD5 checksums of decoded audio waveforms to query PostgreSQL. Existing tracks skip heavy Demucs stem separation and Librosa extraction entirely.
- **Automatic CUE Parsing & Single-Track Fallback**: Automatically parses embedded or external CUE sheet boundaries into individual track tasks. Gracefully falls back to single-track processing if no CUE sheet is present or parsing fails.
- **Native Array Preservation for Multi-Value Tags**: Preserves multi-value VorbisComment tags (such as multiple `ARTIST` or `GENRE` tags) as native JSON arrays (`["...", "..."]`) in the PostgreSQL `meta` (JSONB) column without flattening them into concatenated strings.
- **PostgreSQL JSONB & DLQ Fallback**: Extracted data is asynchronously UPSERTed as JSONB documents. In case of database connection failures, payloads drop into a local SQLite Dead Letter Queue (`send_failed.db`) for safe retry upon recovery.
- **Stale Task Auto-Recovery**: On startup, the Go orchestrator automatically detects tasks stuck in `RUNNING` or `PENDING` due to prior crashes or abrupt halts and resets them to `FAILED`, preventing accidental task skipping.
- **Automated Temp Cache Cleanup**: Removes intermediate precache files (`flac_analyzer_cache`) automatically upon task completion or DLQ fallback, preventing RAM disk or storage depletion.
- **Timestamp Preservation**: Accurately preserves and restores file timestamps (CreationTime, LastWriteTime) whenever modifying FLAC tags (VorbisComment).

### 📚 Documentation
| Document | Content |
|:---|:---|
| [State Diagram](docs/state_diagram.md) | Overall task execution state flow of the pipeline |
| [ERD & Data Structures](docs/database_er_diagram.md) | PostgreSQL/SQLite schemas and JSONB specifications |
| [SHM & WORM Architecture](docs/shm_architecture.md) | Windows Shared Memory management & Zero-Copy IPC |
| [CPU Parallelism & RAM Guard](docs/cpu_parallelism_and_ram_guard.md) | Worker parallel execution & Memory backpressure |
| [CUE Parsing Flow](docs/cue_parsing_flow.md) | CUE sheet parsing & Single-track fallback |
| [DLQ & Error Recovery](docs/dlq_error_recovery.md) | Dead Letter Queue & Stale task recovery |
| [GPU/RAM Fallback Strategy](docs/gpu_fallback_and_ram_defense.md) | CUDA-to-CPU automatic fallback & RAM protection |

---

## State Diagram

Provides a comprehensive flow diagram detailing the state transitions between the Go orchestrator and Python workers.
For the detailed state diagram and execution logic, see [State Diagram (docs/state_diagram.md)](docs/state_diagram.md).

---

## ER Diagram & Data Structures

Details the entity-relationship structures for PostgreSQL and SQLite tables along with JSONB schema details.
For the detailed ER diagram and schema specifications, see [ERD & Data Structures (docs/database_er_diagram.md)](docs/database_er_diagram.md).

---

## Windows Shared Memory (SHM) Management & WORM Architecture

Describes Win32 API orchestration and the Write-Once Read-Many (WORM) shared memory architecture designed to avoid memory exhaustion during large-scale processing on Windows.
For the detailed architecture and lifecycle management, see [SHM & WORM Architecture (docs/shm_architecture.md)](docs/shm_architecture.md).

---

## License

The source code of this project is licensed under the [MIT License](LICENSE).

> [!WARNING]
> **Notice Regarding Pre-trained AI Models (ONNX)**
> This repository contains source code only and does NOT include any pre-trained model weights.
> External models fetched or used by this tool (e.g., Essentia ONNX models, Discogs classifiers) may be subject to their original licensing terms, such as **AGPLv3** (by MTG / Music Technology Group UPF) or Creative Commons licenses.
> Users are responsible for checking and complying with the licensing terms of any third-party models when redistributing or using them for commercial purposes.
