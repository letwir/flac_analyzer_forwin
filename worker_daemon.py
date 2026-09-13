"""
Mor(DaemonRequest -> DaemonResponse)
Functor(f o g) | Semantics(Category: Long-lived Resident Worker Daemon)

worker_daemon.py
================
Go オーケストレーターと stdin/stdout (NDJSON) を介して常駐通信し、
プロセス起動オーバーヘッド（import torch, librosa, onnxruntime の 1〜2秒/曲）を
完全にゼロ化する常駐型ワーカーデーモンですわ！
"""

import json
import logging
import os
import sys
import time
import traceback
from typing import Any
import numpy as np
import torch

# プロジェクト内モジュールの事前ロード (起動時 1 回のみ)
import shm_interop
from analyzer import AudioContext, STEM_CONFIGS, librosa_extractor, extract_mel_patches, run_essentia_serialized, extract_tensor_features
from analyzer.config_generator import load_analyzer_toml

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [WorkerDaemon] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)] # stdout は Go との JSON 通信用に厳格保護
    )

def _open_stem_samples(info: dict[str, Any]) -> tuple[Any, np.ndarray]:
    """Attach one frozen stem and leave cleanup to the owning feature lane."""
    storage_type = info.get("storage_type", "shm")
    file_path = info.get("file_path")
    if storage_type == "file" and file_path and os.path.exists(file_path):
        return None, np.load(file_path, mmap_mode="r")
    return shm_interop.attach_shm_read_only(
        info.get("shm_tag", ""),
        tuple(info["shape"]),
        info["dtype"],
        file_size=info.get("file_size", 0),
    )


def _close_stem_samples(shm: Any, y_np: np.ndarray) -> None:
    """Release either SHM or mmap handles even when a lane fails mid-stem."""
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
        if stem_name == "mix":
            nested["mix"] = features
        else:
            nested["demucs"][stem_name] = features
    return nested


def _serialize_librosa_features(raw_features: Any, track_hash: str) -> Any:
    if hasattr(raw_features, "to_postgres_dict"):
        return raw_features.to_postgres_dict(track_id=track_hash)
    import dataclasses
    if dataclasses.is_dataclass(raw_features):
        return dataclasses.asdict(raw_features)
    return str(raw_features)


def handle_extract_cpu(payload: dict[str, Any], essentia_models: dict) -> dict[str, Any]:
    """Run the single bounded CPU lane: Librosa for every stem and Essentia for mix."""
    sr = payload["sr"]
    stems_info = payload["stems"]
    track_hash = payload.get("track_hash", "dummy_hash")

    t_start = time.perf_counter()
    extracted_librosa: dict[str, Any] = {}
    extracted_essentia: dict[str, Any] = {}

    librosa_total_sec = 0.0
    essentia_total_sec = 0.0

    for stem_name, info in stems_info.items():
        spectro_path = info.get("spectro_path")
        shm, y_np = _open_stem_samples(info)
        try:
            ctx = AudioContext(y=y_np, sr=sr, source=stem_name, spectro_path=spectro_path)
            try:
                t_lib_start = time.perf_counter()
                raw_features = librosa_extractor.run(ctx)
                extracted_librosa[stem_name] = _serialize_librosa_features(raw_features, track_hash)
                librosa_total_sec += time.perf_counter() - t_lib_start

                if stem_name == "mix" and essentia_models:
                    t_ess_start = time.perf_counter()
                    patches = extract_mel_patches(y_np, sr, n_patches=64)
                    extracted_essentia = run_essentia_serialized(patches, essentia_models)
                    essentia_total_sec += time.perf_counter() - t_ess_start
            finally:
                ctx.clear()
        finally:
            _close_stem_samples(shm, y_np)
            del y_np

    return {
        "status": "success",
        "librosa": _nest_stem_features(extracted_librosa),
        "essentia": extracted_essentia,
        "profile": {
            "cpu_total_sec": time.perf_counter() - t_start,
            "librosa_sec": librosa_total_sec,
            "essentia_sec": essentia_total_sec,
        },
    }


def handle_extract_gpu(payload: dict[str, Any], device: Any) -> dict[str, Any]:
    """Run the single bounded GPU lane: Tensor features for every stem."""
    sr = payload["sr"]
    stems_info = payload["stems"]
    t_start = time.perf_counter()
    extracted_tensor: dict[str, Any] = {}
    tensor_total_sec = 0.0

    for stem_name, info in stems_info.items():
        shm, y_np = _open_stem_samples(info)
        y_tensor = None
        try:
            t_ten_start = time.perf_counter()
            with torch.no_grad():
                y_tensor = torch.from_numpy(np.require(y_np, requirements=["C", "W"]))
                extracted_tensor[stem_name] = extract_tensor_features(
                    y_tensor, sr, device, spectro_path=info.get("spectro_path")
                )
            tensor_total_sec += time.perf_counter() - t_ten_start
        finally:
            del y_tensor
            _close_stem_samples(shm, y_np)
            del y_np

    return {
        "status": "success",
        "tensor": _nest_stem_features(extracted_tensor),
        "profile": {
            "gpu_total_sec": time.perf_counter() - t_start,
            "tensor_sec": tensor_total_sec,
        },
    }


def handle_extract_all(payload: dict[str, Any], essentia_models: dict, device: Any) -> dict[str, Any]:
    """Keep the legacy extract_all protocol while delegating to both lane handlers."""
    t_start = time.perf_counter()
    cpu_response = handle_extract_cpu(payload, essentia_models)
    gpu_response = handle_extract_gpu(payload, device)

    return {
        "status": "success",
        "librosa": cpu_response["librosa"],
        "tensor": gpu_response["tensor"],
        "essentia": cpu_response["essentia"],
        "profile": {
            "extract_total_sec": time.perf_counter() - t_start,
            "librosa_sec": cpu_response["profile"]["librosa_sec"],
            "tensor_sec": gpu_response["profile"]["tensor_sec"],
            "essentia_sec": cpu_response["profile"]["essentia_sec"],
        },
    }

def main():
    setup_logger()
    logger = logging.getLogger("WorkerDaemon")
    logger.info("常駐型ワーカーデーモンを起動いたしましたわ！ モデルと環境を事前初期化いたしますの。")

    try:
        load_analyzer_toml()
    except Exception as e:
        logger.warning(f"[SafetyGuard] analyzer.toml ロード警告: {e}")

    # PyTorch デバイス
    import torch
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    logger.info(f"PyTorch 演算デバイス: {device}")

    # Essentia モデルの事前初期化
    models_dir = os.path.abspath(os.path.join(os.path.dirname(__file__), "models"))
    essentia_models = {}
    if os.path.exists(models_dir):
        import models
        essentia_models = models.init_worker_onnx(models_dir)
        logger.info(f"Essentia ONNX モデル初期化完了 (分類器数: {len(essentia_models)})")

    logger.info("Go オーケストレーターからのリクエスト待機ループ (NDJSON) を開始いたしますわ！")

    # 初期化完了シグナル (Go が検知可能)
    ready_signal = json.dumps({"status": "ready", "device": str(device)})
    sys.stdout.write(ready_signal + "\n")
    sys.stdout.flush()

    task_count = 0
    max_tasks_before_recycle = 100

    # リクエスト処理ループ
    for line in sys.stdin:
        line_strip = line.strip()
        if not line_strip:
            continue

        try:
            req = json.loads(line_strip)
            req_id = req.get("id", "req-0")
            action = req.get("action", "extract_all")

            if action == "ping":
                resp = {"id": req_id, "status": "pong"}
            elif action == "extract_all":
                resp = handle_extract_all(req["payload"], essentia_models, device)
                resp["id"] = req_id
                task_count += 1
            elif action == "extract_cpu":
                resp = handle_extract_cpu(req["payload"], essentia_models)
                resp["id"] = req_id
                task_count += 1
            elif action == "extract_gpu":
                resp = handle_extract_gpu(req["payload"], device)
                resp["id"] = req_id
                task_count += 1
            else:
                resp = {"id": req_id, "status": "error", "message": f"Unknown action: {action}"}

        except Exception as e:
            logger.exception(f"リクエスト処理中に例外が発生いたしましたわ: {e}")
            resp = {
                "id": req.get("id", "unknown") if 'req' in locals() else "unknown",
                "status": "error",
                "error": str(e),
                "traceback": traceback.format_exc()
            }

        # レスポンス返却
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()

        # メモリ健全性のための定期 GC & VRAM キャッシュ解放 (ADV-02: 副作用をメインループ層へ集約)
        if task_count > 0 and task_count % 10 == 0:
            import gc
            gc.collect()
            if device.type == "cuda":
                torch.cuda.empty_cache()

        # Graceful Recycling の通知
        if task_count >= max_tasks_before_recycle:
            logger.info(f"処理タスク数が上限 ({max_tasks_before_recycle}) に達しましたわ。プロセスを正常終了して再生成を促しますの。")
            break

    logger.info("ワーカーデーモンを正常に停止いたしますわ。")
    sys.exit(0)

if __name__ == "__main__":
    main()
