import os
import sys
import time
import json
import glob
import psutil
import subprocess
import requests
import threading

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
    
    for f in glob.glob(os.path.join(out_dir, "*.json")):
        try:
            os.remove(f)
        except:
            pass

    orchestrator_exe = os.path.join("orchestrator", "orchestrator.exe")
    if not os.path.exists(orchestrator_exe):
        print(f"Orchestrator executable not found at {orchestrator_exe}. Please build it first.")
        return

    print("Starting Go Orchestrator with --no-db flag...")
    orch_proc = subprocess.Popen(
        [orchestrator_exe, "--no-db"],
        cwd="orchestrator",
        stdout=sys.stdout,
        stderr=sys.stderr
    )

    time.sleep(2)
    
    if orch_proc.poll() is not None:
        print("Orchestrator failed to start.")
        return

    stop_monitoring = threading.Event()
    max_memory_result = []
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

    expected_jsons = len(flac_files)
    completed_jsons = 0
    
    print("Waiting for tasks to complete...")
    try:
        while True:
            if orch_proc.poll() is not None:
                print("Orchestrator process exited unexpectedly! Possible OOM or crash.")
                break
                
            jsons = glob.glob(os.path.join(out_dir, "*.json"))
            completed_jsons = len(jsons)
            if completed_jsons >= expected_jsons:
                print(f"All {expected_jsons} tasks completed successfully.")
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
    print(f"Processed Files      : {completed_jsons} / {expected_jsons}")
    
    if completed_jsons == expected_jsons:
        print("STATUS: SUCCESS")
    else:
        print("STATUS: FAILED (Not all outputs were generated)")

if __name__ == "__main__":
    sys.stdout.reconfigure(encoding='utf-8')
    run_integration_test()
