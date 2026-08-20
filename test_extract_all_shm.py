import subprocess
import json
import sys
import os
import time
import numpy as np
import shm_interop

print(f"Python: {sys.executable}", flush=True)

# 1. Create dummy audio data for 7 stems in Shared Memory
sr = 44100
dur_sec = 5
n_samples = sr * dur_sec

stems = ["mix", "bass", "drums", "vocals", "other", "guitar", "piano"]
stems_meta = {}

t = np.linspace(0, dur_sec, n_samples, dtype=np.float32)
dummy_wave = (0.5 * np.sin(2 * np.pi * 440 * t)).astype(np.float32)
dummy_data = dummy_wave[np.newaxis, :]  # (1, N)

shm_handles = []
for stem in stems:
    tag = f"Local\\TestShm_{stem}"
    shm = shm_interop.write_to_shm(tag, dummy_data)
    shm.close() # Close in writer (Demucs style)
    stems_meta[stem] = {
        "shm_tag": tag,
        "shape": list(dummy_data.shape),
        "dtype": "float32",
        "file_size": 0
    }

print("Created 7 SHM arenas successfully.", flush=True)

# 2. Spawn worker_daemon.py
p = subprocess.Popen(
    [sys.executable, "-u", "worker_daemon.py"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True
)

import threading
def print_stderr():
    for line in p.stderr:
        print(f"[DAEMON-STDERR] {line.strip()}", flush=True)

t = threading.Thread(target=print_stderr, daemon=True)
t.start()

print("Waiting for ready signal...", flush=True)
ready = p.stdout.readline()
print("Ready:", ready.strip(), flush=True)

# 3. Send extract_all request
req = {
    "id": "req-test-1",
    "action": "extract_all",
    "payload": {
        "sr": sr,
        "track_hash": "test_hash_12345",
        "stems": stems_meta
    }
}

print("Sending extract_all request...", flush=True)
t0 = time.time()
p.stdin.write(json.dumps(req) + "\n")
p.stdin.flush()

print("Waiting for extract_all response...", flush=True)
resp_line = p.stdout.readline()
t_elapsed = time.time() - t0
print(f"Received response in {t_elapsed:.2f}s!", flush=True)

try:
    resp = json.loads(resp_line)
    print("Response status:", resp.get("status"))
    print("Profile:", resp.get("profile"))
except Exception as e:
    print("Failed to parse JSON response:", e, "Raw:", resp_line)

p.stdin.close()
p.wait(timeout=5)
print("Test completed successfully.", flush=True)
