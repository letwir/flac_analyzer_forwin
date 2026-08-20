import subprocess
import json
import sys
import os

print(f"Using python: {sys.executable}", flush=True)
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

print("Waiting for ready signal from daemon...", flush=True)
ready = p.stdout.readline()
print("Ready signal received:", ready.strip(), flush=True)

print("Sending ping...", flush=True)
p.stdin.write(json.dumps({"id": "test-1", "action": "ping"}) + "\n")
p.stdin.flush()

pong = p.stdout.readline()
print("Pong response:", pong.strip(), flush=True)

p.stdin.close()
p.wait(timeout=5)
print("Daemon exited cleanly.", flush=True)
