"""CPU-only persistent feature daemon. It never imports or initializes CUDA."""

import dataclasses
import json
import logging
import os
import sys
import time
import traceback
from typing import Any

# This must precede all third-party imports; the CPU role has no CUDA runtime.
os.environ["CUDA_VISIBLE_DEVICES"] = "-1"

import numpy as np

import shm_interop
from analyzer import AudioContext, extract_mel_patches, librosa_extractor, run_essentia_serialized
from analyzer.config_generator import load_analyzer_toml


def setup_logger() -> None:
    logging.basicConfig(level=logging.INFO, format="[%(levelname)s] [WorkerCPU] %(message)s", handlers=[logging.StreamHandler(sys.stderr)])


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


def _serialize_librosa_features(raw_features: Any, track_hash: str) -> Any:
    if hasattr(raw_features, "to_postgres_dict"):
        return raw_features.to_postgres_dict(track_id=track_hash)
    if dataclasses.is_dataclass(raw_features):
        return dataclasses.asdict(raw_features)
    return str(raw_features)


def handle_extract_cpu(payload: dict[str, Any], essentia_models: dict) -> dict[str, Any]:
    sr, stems_info, track_hash = payload["sr"], payload["stems"], payload.get("track_hash", "dummy_hash")
    started = time.perf_counter()
    extracted_librosa: dict[str, Any] = {}
    extracted_essentia: dict[str, Any] = {}
    librosa_sec = essentia_sec = 0.0
    for stem_name, info in stems_info.items():
        shm, y_np = _open_stem_samples(info)
        try:
            ctx = AudioContext(y=y_np, sr=sr, source=stem_name, spectro_path=info.get("spectro_path"))
            try:
                step = time.perf_counter()
                extracted_librosa[stem_name] = _serialize_librosa_features(librosa_extractor.run(ctx), track_hash)
                librosa_sec += time.perf_counter() - step
                if stem_name == "mix" and essentia_models:
                    step = time.perf_counter()
                    extracted_essentia = run_essentia_serialized(extract_mel_patches(y_np, sr, n_patches=64), essentia_models)
                    essentia_sec += time.perf_counter() - step
            finally:
                ctx.clear()
        finally:
            _close_stem_samples(shm, y_np)
    return {"status": "success", "librosa": _nest_stem_features(extracted_librosa), "essentia": extracted_essentia, "profile": {"cpu_total_sec": time.perf_counter() - started, "librosa_sec": librosa_sec, "essentia_sec": essentia_sec}}


def main() -> None:
    setup_logger()
    try:
        load_analyzer_toml()
    except Exception as exc:
        logging.warning("analyzer.toml load warning: %s", exc)
    essentia_models: dict = {}
    models_dir = os.path.join(os.path.dirname(__file__), "models")
    if os.path.exists(models_dir):
        import models
        essentia_models = models.init_worker_onnx(models_dir, providers_override=["CPUExecutionProvider"])
    sys.stdout.write(json.dumps({"status": "ready", "role": "cpu", "device": "cpu"}) + "\n")
    sys.stdout.flush()
    task_count = 0
    for line in sys.stdin:
        try:
            req = json.loads(line)
            if req.get("action") == "ping":
                response = {"id": req.get("id", "ping"), "status": "pong"}
            elif req.get("action") == "extract_cpu":
                response = handle_extract_cpu(req["payload"], essentia_models)
                response["id"] = req.get("id", "unknown")
                task_count += 1
            else:
                response = {"id": req.get("id", "unknown"), "status": "error", "error": "CPU daemon only accepts extract_cpu"}
        except Exception as exc:
            logging.exception("request failed: %s", exc)
            response = {"id": req.get("id", "unknown") if "req" in locals() else "unknown", "status": "error", "error": str(exc), "traceback": traceback.format_exc()}
        sys.stdout.write(json.dumps(response) + "\n")
        sys.stdout.flush()
        if task_count >= 100:
            break


if __name__ == "__main__":
    main()
