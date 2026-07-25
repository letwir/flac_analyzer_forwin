import os
import sys
import time
import json
import glob
import psutil
import subprocess
import requests
import threading
import sqlite3
import shutil

def monitor_process_tree(pid, stop_event, result_list):
    max_memory = 0
    try:
        parent = psutil.Process(pid)
    except psutil.NoSuchProcess:
        print("Orchestrator process not found for monitoring.")
        result_list.append(0)
        return

    while not stop_event.is_set():
        try:
            total_memory = 0
            total_memory += parent.memory_info().rss
            for child in parent.children(recursive=True):
                total_memory += child.memory_info().rss
            
            if total_memory > max_memory:
                max_memory = total_memory
                
            time.sleep(0.5)
        except psutil.NoSuchProcess:
            break
        except Exception:
            time.sleep(0.5)
            continue
            
    result_list.append(max_memory)

def run_integration_test():
    print("=== Starting Integration & OOM Monitoring Test ===")
    
    test_dir = os.path.join("testFLAC", "test")
    out_dir = "testFLAC"
    
    flac_files = glob.glob(os.path.join(test_dir, "*.flac"))
    if not flac_files:
        print(f"No FLAC files found in {test_dir}. Please add some for testing.")
        return

    print(f"Found {len(flac_files)} FLAC files for testing.")
    
    # 1. config.toml の一時置換とバックアップ
    config_orig = "config.toml"
    config_bak = "config.toml.bak"
    config_test = "config_test.toml"
    
    if not os.path.exists(config_test):
        print(f"Test config not found: {config_test}")
        return
        
    print("Backing up config.toml and applying config_test.toml...")
    if os.path.exists(config_orig):
        shutil.copyfile(config_orig, config_bak)
    shutil.copyfile(config_test, config_orig)
    
    # Remove existing orchestrator.db to ensure all tasks are run fresh
    db_file = os.path.join("orchestrator", "orchestrator.db")
    if os.path.exists(db_file):
        try:
            os.remove(db_file)
            print(f"Removed existing state database: {db_file}")
        except Exception as e:
            print(f"Warning: failed to remove {db_file}: {e}")

    for f in glob.glob(os.path.join(out_dir, "*.json")):
        try:
            os.remove(f)
        except:
            pass

    orchestrator_exe = os.path.join("orchestrator", "orchestrator.exe")
    if not os.path.exists(orchestrator_exe):
        print(f"Orchestrator executable not found at {orchestrator_exe}. Please build it first.")
        # Restore config.toml
        if os.path.exists(config_bak):
            shutil.move(config_bak, config_orig)
        return

    orch_proc = None
    monitor_thread = None
    stop_monitoring = threading.Event()
    max_memory_result = []

    try:
        print("Starting Go Orchestrator...")
        # Since config.toml is temporarily overwritten with test config, run without flags
        orch_proc = subprocess.Popen(
            [orchestrator_exe],
            cwd="orchestrator",
            stdout=sys.stdout,
            stderr=sys.stderr
        )

        time.sleep(2)
        
        if orch_proc.poll() is not None:
            print("Orchestrator failed to start.")
            return

        monitor_thread = threading.Thread(target=monitor_process_tree, args=(orch_proc.pid, stop_monitoring, max_memory_result))
        monitor_thread.start()

        start_time = time.time()
        
        for flac_path in flac_files:
            abs_flac_path = os.path.abspath(flac_path)
            file_size = os.path.getsize(abs_flac_path)
            payload = {
                "flacPath": abs_flac_path,
                "fileSize": file_size,
                "targetScript": ""
            }
            try:
                resp = requests.post("http://localhost:8080/task", json=payload)
                if resp.status_code == 202:
                    print(f"Submitted task for {os.path.basename(flac_path)}")
                else:
                    print(f"Failed to submit task for {flac_path}: {resp.status_code} {resp.text}")
            except Exception as e:
                print(f"Error submitting task: {e}")

        expected_tasks = len(flac_files)
        completed_tasks = 0
        
        print("Waiting for tasks to complete...")
        try:
            while True:
                if orch_proc.poll() is not None:
                    print("Orchestrator process exited unexpectedly! Possible OOM or crash.")
                    break
                    
                # Query progress from SQLite
                completed_tasks = 0
                if os.path.exists(db_file):
                    try:
                        conn = sqlite3.connect(db_file)
                        cur = conn.cursor()
                        cur.execute("SELECT COUNT(*) FROM task_state WHERE status IN ('COMPLETED', 'FAILED')")
                        completed_tasks = cur.fetchone()[0]
                        conn.close()
                    except Exception:
                        pass
                
                print(f"Progress: {completed_tasks} / {expected_tasks} tasks done")
                if completed_tasks >= expected_tasks:
                    print(f"All {expected_tasks} tasks processed.")
                    break
                    
                time.sleep(2)
        except KeyboardInterrupt:
            print("Test interrupted by user.")

        end_time = time.time()
        execution_time = end_time - start_time
        
        stop_monitoring.set()
        monitor_thread.join()
        
        try:
            orch_proc.terminate()
            orch_proc.wait(timeout=5)
        except Exception:
            orch_proc.kill()

        max_mem_bytes = max_memory_result[0] if max_memory_result else 0
        max_mem_gb = max_mem_bytes / (1024 ** 3)
        
        print("\n=== Test Results ===")
        print(f"Total Execution Time : {execution_time:.2f} seconds")
        print(f"Peak Memory Usage    : {max_mem_gb:.2f} GB")
        print(f"Processed Tasks      : {completed_tasks} / {expected_tasks}")
        
        if completed_tasks == expected_tasks:
            print("STATUS: SUCCESS")
        else:
            print("STATUS: FAILED (Not all tasks completed)")

    finally:
        print("Restoring config.toml from backup...")
        if os.path.exists(config_bak):
            if os.path.exists(config_orig):
                os.remove(config_orig)
            os.rename(config_bak, config_orig)

if __name__ == "__main__":
    sys.stdout.reconfigure(encoding='utf-8')
    run_integration_test()
