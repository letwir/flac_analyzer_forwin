"""
Mor(DaemonRequest -> DaemonResponse)
Functor(f o g) | Semantics(Category: Long-lived Resident Demucs GPU Worker Daemon)

demucs_daemon.py
================
Go オーケストレーターと stdin/stdout (NDJSON) を介して常駐通信し、
毎回の Python 起動オーバーヘッドおよび Demucs ONNX モデルの VRAM ロード時間を
完全にゼロ化する常駐型波形分離デーモンですわ！
"""

import json
import logging
import os
import sys
import time
import traceback
from typing import Any

# Windows環境で .venv 内の nvidia ディレクトリの bin を動的追加
if sys.platform == "win32":
    nvidia_base = os.path.join(sys.prefix, "Lib", "site-packages", "nvidia")
    if os.path.exists(nvidia_base):
        for root, dirs, _ in os.walk(nvidia_base):
            if "bin" in dirs:
                bin_path = os.path.join(root, "bin")
                try:
                    os.add_dll_directory(bin_path)
                except Exception:
                    pass
                os.environ["PATH"] = bin_path + os.pathsep + os.environ.get("PATH", "")

import numpy as np
import shm_interop
import models
from flac_decode import build_flac_handle, process_slice_with_seq_safety

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [DemucsDaemon] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)] # stdout は Go との JSON 通信用に厳格保護
    )

def handle_check_hash(payload: dict[str, Any]) -> dict[str, Any]:
    """
    FLAC 楽曲の指定範囲を軽量デコードし、MD5 ハッシュのみを高速算出する純粋射ですわ！
    """
    flac_path = payload["flac_path"]
    start_sample = payload.get("start_sample", 0)
    end_sample = payload.get("end_sample", -1)

    t_dec_start = time.perf_counter()
    handle = build_flac_handle(flac_path)
    start_samp = start_sample
    end_samp = end_sample if end_sample != -1 else handle.total_samples

    _, md5_hash = process_slice_with_seq_safety(
        flac_path,
        start_samp,
        end_samp,
        handle.sample_rate,
        handle.channels
    )
    decode_dur = time.perf_counter() - t_dec_start

    return {
        "status": "success",
        "audio_hash": md5_hash,
        "profile": {
            "decode": decode_dur
        }
    }

def handleSeparateTaskHeavy(payload: dict[str, Any], separator: Any) -> dict[str, Any]:
    """
    常駐 Demucs モデルを用いて波形分離を実行し、共有メモリへ書き込む高負荷射ですわ！
    Advisory 1 遵守: 書き込み後、レスポンス送信前に shm.close() を徹底し Error 1450 を防ぎますの。
    """
    flac_path = payload["flac_path"]
    shm_tags = payload.get("shm_tags", {})
    start_sample = payload.get("start_sample", 0)
    end_sample = payload.get("end_sample", -1)

    t_dec_start = time.perf_counter()
    handle = build_flac_handle(flac_path)
    start_samp = start_sample
    end_samp = end_sample if end_sample != -1 else handle.total_samples

    y, md5_hash = process_slice_with_seq_safety(
        flac_path,
        start_samp,
        end_samp,
        handle.sample_rate,
        handle.channels
    )
    decode_duration = time.perf_counter() - t_dec_start

    y_stereo = y.T  # demucs expects (channels, samples)
    sr = 44100

    t_inf_start = time.perf_counter()
    stem_context = separator.separate(y_stereo, sr)
    inference_duration = time.perf_counter() - t_inf_start

    storage_mode = payload.get("storage_mode", "shm")
    temp_dir = payload.get("temp_dir", "")

    stems_meta: dict[str, Any] = {}
    t_storage_start = time.perf_counter()

    file_size = os.path.getsize(flac_path) if os.path.exists(flac_path) else 0
    stem_items = stem_context.stems.items() if hasattr(stem_context, "stems") else stem_context.items()

    if storage_mode == "disk" and temp_dir:
        os.makedirs(temp_dir, exist_ok=True)
        for stem_name, audio_ctx in stem_items:
            data = audio_ctx.y
            if data.ndim == 1:
                data = data[np.newaxis, :]
            data = np.ascontiguousarray(data, dtype=np.float32)

            file_path = os.path.join(temp_dir, f"{stem_name}.npy")
            np.save(file_path, data)

            stems_meta[stem_name] = {
                "storage_type": "file",
                "file_path": file_path,
                "shape": list(data.shape),
                "dtype": str(data.dtype),
                "file_size": file_size
            }
    else:
        # ステム波形の共有メモリ書き込み (SHM Mode)
        for stem_name, audio_ctx in stem_items:
            if stem_name not in shm_tags:
                continue
            tag = shm_tags[stem_name]
            data = audio_ctx.y

            # (1, N) または (N,) を float32 形状に整えますわ
            if data.ndim == 1:
                data = data[np.newaxis, :]
            data = np.ascontiguousarray(data, dtype=np.float32)

            # 共有メモリへ書き込み、完了後即座にアンマップ (Advisory 1: Error 1450 完全防止)
            shm = shm_interop.write_to_shm(tag, data, file_size=file_size)
            shm.close()

            stems_meta[stem_name] = {
                "storage_type": "shm",
                "shm_tag": tag,
                "shape": list(data.shape),
                "dtype": str(data.dtype),
                "file_size": file_size
            }

    storage_duration = time.perf_counter() - t_storage_start

    return {
        "status": "success",
        "audio_hash": md5_hash,
        "sr": sr,
        "stems": stems_meta,
        "profile": {
            "decode": decode_duration,
            "inference": inference_duration,
            "storage_write": storage_duration
        }
    }

def runDemucsDaemonLoopComplex():
    """
    常駐 Demucs デーモンのメイン IPC ループですわ！
    """
    setup_logger()
    logger = logging.getLogger("DemucsDaemon")
    logger.info("Demucs 常駐ワーカーデーモンを起動いたしましたわ！ モデルを GPU VRAM にロードいたしますの。")

    # Demucs モデルの事前ロード
    use_dml = "--use-dml" in sys.argv
    models.init_global_demucs(use_dml=use_dml)
    separator = models.GLOBAL_DEMUCS

    logger.info(f"Demucs モデルの初期化が完了いたしましたわ！ Go からのリクエスト待機に入りますの。")

    # 起動準備完了シグナル (Ready)
    print(json.dumps({"status": "ready", "device": "gpu"}), flush=True)

    for line in sys.stdin:
        clean_line = line.strip()
        if not clean_line:
            continue

        try:
            req = json.loads(clean_line)
            cmd = req.get("command", "")
            payload = req.get("payload", {})

            if cmd == "ping":
                resp = {"status": "pong", "time": time.time()}
            elif cmd == "shutdown":
                resp = {"status": "bye"}
                print(json.dumps(resp), flush=True)
                break
            elif cmd == "check_hash":
                resp = handle_check_hash(payload)
            elif cmd == "separate":
                resp = handleSeparateTaskHeavy(payload, separator)
            else:
                resp = {"status": "error", "message": f"Unknown command: {cmd}"}

            print(json.dumps(resp), flush=True)

        except Exception as e:
            logger.exception("Demucs タスク処理中に例外が発生いたしましたわ！")
            err_resp = {
                "status": "error",
                "message": str(e),
                "traceback": traceback.format_exc()
            }
            print(json.dumps(err_resp), flush=True)

if __name__ == "__main__":
    runDemucsDaemonLoopComplex()
