<methods>
  <target id="ESSENTIA_SEGFAULT_PREVENTION">
    <why>ONNX Runtime parallel access and OpenMP/Python thread collision cause SegFaults.</why>
  </target>
  <target id="LIBROSA_CATEGORICAL_DESIGN">
    <why>To cleanly structure Librosa analysis by introducing Category Theory (Applicative/Product) for dynamic context switching.</why>
  </target>
  <target id="LAZY_PROPERTY_CACHE_CSE">
    <why>Heavy DSP (STFT, Mel, Chroma) is computed redundantly. Caching eliminates overhead.</why>
  </target>
  <target id="GLOBAL_DEMUCS_STEMCONTEXT">
    <why>Decouple demucs logic and feature extraction. Prevent multiple model loads.</why>
  </target>
  <target id="SEPARATE_FLAC_AND_JSONB">
    <why>Decouple 100x rounded FLAC tags from pure float Postgres JSONB to avoid DDL changes on new features.</why>
  </target>
  <target id="LIBROSA_HNR">
    <why>Need quantitative metric (0.0-1.0) for harmonic purity (instrument sparseness/vocal clarity) using Autocorrelation Peak.</why>
  </target>
  <target id="STEM_RELATIVE_SNR">
    <why>Replace primitive pre-emphasis SNR with true musical SDR/SNR using Demucs stems for mix analysis. Preserve categorical purity by calculating SNR in a Post-processing phase.</why>
  </target>
  <target id="ENHANCED_MIR_FEATURES">
    <why>Elevate MIR value (Chroma, HPSS, Spectral Flux, Onset Density, Tempogram, Dynamic Range, MFCCx20) for advanced musical profiling.</why>
  </target>
  <target id="COMONAD_AUDIO_HASH">
    <why>Ensure unique deterministic ID generation for 80k+ tracks (including Embedded CUE segments) immune to tag modifications without polluting DSP logic.</why>
    <how>Calculate the MD5 hash directly from the decoded raw PCM bytes of the sliced audio segment (via flac_decode / process_slice_with_seq_safety), NEVER from the whole file binary. This guarantees that tracks extracted from a CUE sheet receive distinct and stable hashes, while remaining completely invariant to metadata tag edits.</how>
  </target>
  <target id="CUESHEET_FLATTENED_SCHEMA_V2">
    <why>Maximize Cuesheet extraction robustness (3-tier fallback). Flatten JSONB to columns to avoid index overhead and enable high-performance B-Tree lookups.</why>
  </target>
  <target id="STEREO_DOWNMIX_GUARD">
    <why>Demucs requires stereo for spatial precision, but Librosa misinterprets (samples, channels) as (channels, samples). Need early downmix guard before Essentia.</why>
  </target>
  <target id="MUTAGEN_METADATA_MERGE_FILTER">
    <why>To map VorbisComment to Postgres `meta` JSONB while ensuring structural uniformity between single-track and multi-track (Cuesheet) processing (Morphism consistency).</why>
  </target>
  <target id="DRUMS_BASS_TEMPOBEAT_EXTENSION">
    <why>Enable groove analysis for drums/bass. Strict evaluation during Pre-warming prevents GIL/LIBROSA_LOCK contention and preserves lazy evaluation safety.</why>
  </target>
  <target id="ZERO_COPY_DECOUPLED_PIPELINE">
    <why>Python processes holding onto huge audio arrays across extraction steps leads to memory bloat and violates Referential Transparency. We must separate effectful writing from pure reading.</why>
    <how>Create separate `demucs_worker.py` and `librosa_worker.py`. Demucs writes output to a Go-provided Shared Memory handle. The worker cleanly exits (exit 0) as a completion signal. Go then applies VirtualProtect (`Freeze()`) to the memory region, enforcing Write-Once-Read-Many (WORM), and invokes the Librosa worker which attaches to the memory segment as Read-Only. This models state transition as an explicit morphism.</how>
  </target>
  <target id="SINGLE_PROCESS_FLAC_ISOLATION">
    <why>並列 P/C パイプラインでのマルチプロセス起動および SharedMemory 状態共有が、Windows環境における深刻な RAM リークおよび OOM を引き起こすため、Python プロセスをファイル単位で完全に分離・破棄したいからですわ。</why>
    <how>PowerShell側ですべての FLAC ファイルパスを再帰的に列挙して一次保存（配列保持）し、ループ処理で python main.py &lt;flacfullpath&gt; を1個ずつ同期呼び出ししますの。Python側はインプロセスでデコードから分離、特徴量抽出、DB書き込み、タグ保存までを直列で完結させ、処理終了とともにプロセスを完全に破棄してメモリを全解放しますわ。(※ このパスは ZERO_COPY_DECOUPLED_PIPELINE によって Goオーケストレーションへと昇華しました)</how>
  </target>
  <target id="STATE_MANAGEMENT_SQLITE">
    <why>To prevent redundant task execution and monitor overall pipeline status concurrently without database lockups.</why>
    <how>Use Go standard sql package with github.com/mattn/go-sqlite3. Open the database in WAL mode and transactionally verify status in CheckOrInsert before task dispatching.</how>
  </target>
  <target id="DLQ_FALLBACK_INGESTER">
    <why>To prevent loss of heavy audio feature extraction payloads in case of database unavailability or network faults.</why>
    <how>Catch database connection exceptions in Python's ingester, serialize the payload to SQLite (send_failed.db), and expose retry_ingest.py for batch replay once connectivity restores.</how>
  </target>
  <target id="PROMETHEUS_METRICS_EXPORTER">
    <why>To gain observability into active workers, demucs semaphore slots, and queue lengths under heavy batch processing load.</why>
    <how>Expose a prometheus metrics endpoint using the official prometheus go client library on port 2112, incrementing and decrementing gauges within the Go dispatcher.</how>
  </target>
</methods>
