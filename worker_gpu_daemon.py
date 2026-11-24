"""Dedicated CUDA feature daemon. Startup fails rather than silently using CPU."""

import json
import logging
import os
import sys
import time
import traceback
import warnings
from typing import Any

import gc
import numpy as np
import torch

import shm_interop
from analyzer import extract_tensor_features
from analyzer.tensor_dsp import compute_and_save_spectro_chunked_Large


def _open_stem_samples(info: dict[str, Any]) -> tuple[Any, np.ndarray]:
    if info.get("storage_type", "shm") == "file" and info.get("file_path") and os.path.exists(info["file_path"]):
        return None, np.load(info["file_path"], mmap_mode="r")
    return shm_interop.attach_shm_read_only(info.get("shm_tag", ""), tuple(info["shape"]), info["dtype"], file_size=info.get("file_size", 0))


def _close_stem_samples(shm: Any, y_np: np.ndarray) -> None:
    try:
        if shm is not None:
            shm.close()
    finally:
        mmap_handle = getattr(y_np, "_mmap", None)
        if mmap_handle is not None:
            mmap_handle.close()


def _nest_stem_features(extracted: dict[str, Any]) -> dict[str, Any]:
    nested: dict[str, Any] = {"demucs": {}}
    for stem_name, features in extracted.items():
        (nested if stem_name == "mix" else nested["demucs"])[stem_name] = features
    return nested


def handle_extract_gpu(payload: dict[str, Any], device: torch.device) -> dict[str, Any]:
    started, extracted, spectro_cache, tensor_sec = time.perf_counter(), {}, {}, 0.0
    for stem_name, info in payload["stems"].items():
        shm, y_np = _open_stem_samples(info)
        y_tensor = None
        try:
            step = time.perf_counter()
            with torch.no_grad():
                y_contig = y_np if y_np.flags.c_contiguous else np.ascontiguousarray(y_np)
                with warnings.catch_warnings():
                    warnings.simplefilter("ignore", UserWarning)
                    y_tensor = torch.from_numpy(y_contig)
                extracted[stem_name] = extract_tensor_features(
                    y_tensor,
                    payload["sr"],
                    device,
                    stem_name=stem_name,
                )
                spectro_path = info.get("spectro_path")
                if spectro_path:
                    try:
                        compute_and_save_spectro_chunked_Large(y_tensor, spectro_path, device)
                        spectro_cache[stem_name] = {
                            "status": "written",
                            "spectro_path": spectro_path,
                        }
                    except Exception as cache_exc:
                        logging.warning("Spectro cache generation failed for %s (%s): %s", stem_name, spectro_path, cache_exc)
                        spectro_cache[stem_name] = {
                            "status": "fallback",
                            "error": str(cache_exc),
                        }
                else:
                    spectro_cache[stem_name] = {"status": "skipped"}
            tensor_sec += time.perf_counter() - step
        finally:
            del y_tensor
            _close_stem_samples(shm, y_np)
    return {
        "status": "success",
        "tensor": _nest_stem_features(extracted),
        "spectro_cache": spectro_cache,
        "profile": {"gpu_total_sec": time.perf_counter() - started, "tensor_sec": tensor_sec},
    }


def handle_cleanup_gpu(device: torch.device) -> dict[str, Any]:
    gc.collect()
    if device.type == "cuda" and torch.cuda.is_available():
        torch.cuda.synchronize(device)
    torch.cuda.empty_cache()
    return {"status": "success"}


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="[%(levelname)s] [WorkerGPU] %(message)s", handlers=[logging.StreamHandler(sys.stderr)])
    if not torch.cuda.is_available():
        raise RuntimeError("feature GPU daemon requires CUDA; refusing CPU fallback")
    torch.backends.cuda.matmul.allow_tf32 = True
    torch.backends.cudnn.allow_tf32 = True
    if hasattr(torch, "set_float32_matmul_precision"):
        torch.set_float32_matmul_precision("high")
    device = torch.device("cuda")
    sys.stdout.write(json.dumps({"status": "ready", "role": "feature-gpu", "device": device.type}) + "\n")
    sys.stdout.flush()
    task_count = 0
    for line in sys.stdin:
        try:
            req = json.loads(line)
            if req.get("action") == "ping":
                response = {"id": req.get("id", "ping"), "status": "pong"}
            elif req.get("action") == "extract_gpu":
                response = handle_extract_gpu(req["payload"], device)
                response["id"] = req.get("id", "unknown")
                task_count += 1
            elif req.get("action") == "cleanup_gpu":
                response = handle_cleanup_gpu(device)
                response["id"] = req.get("id", "unknown")
            else:
                response = {"id": req.get("id", "unknown"), "status": "error", "error": "GPU daemon only accepts extract_gpu"}
        except Exception as exc:
            logging.exception("request failed: %s", exc)
            response = {"id": req.get("id", "unknown") if "req" in locals() else "unknown", "status": "error", "error": str(exc), "traceback": traceback.format_exc()}
        sys.stdout.write(json.dumps(response) + "\n")
        sys.stdout.flush()
        if task_count >= 100:
            break


if __name__ == "__main__":
    main()
