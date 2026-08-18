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

def handle_extract_all(payload: dict[str, Any], essentia_models: dict, device: Any) -> dict[str, Any]:
    """
    1 つの楽曲に対する Librosa, Tensor, Essentia の全特徴量を
    共有メモリから Zero-copy で一括抽出し、結果を統合して返却する純粋射ですわ！
    """
    sr = payload["sr"]
    stems_info = payload["stems"]
    track_hash = payload.get("track_hash", "dummy_hash")

    t_start = time.perf_counter()
    extracted_librosa: dict[str, Any] = {}
    extracted_tensor: dict[str, Any] = {}
    extracted_essentia: dict[str, Any] = {}

    import torch

    librosa_total_sec = 0.0
    tensor_total_sec = 0.0
    essentia_total_sec = 0.0

    # 1. 各ステムの処理 (Advisory 2: try...finally shm.close() を徹底)
    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        file_size = info.get("file_size", 0)
        spectro_path = info.get("spectro_path")

        shm, y_np = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name, file_size=file_size)
        try:
            ctx = AudioContext(y=y_np, sr=sr, source=stem_name, spectro_path=spectro_path)
            try:
                # A. Librosa 特徴量抽出 (オンデマンド評価により 49s の無駄な CPU Warmup を完全排除)
                t_lib_start = time.perf_counter()
                raw_features = librosa_extractor.run(ctx)
                if hasattr(raw_features, "to_postgres_dict"):
                    extracted_librosa[stem_name] = raw_features.to_postgres_dict(track_id=track_hash)
                else:
                    import dataclasses
                    if dataclasses.is_dataclass(raw_features):
                        extracted_librosa[stem_name] = dataclasses.asdict(raw_features)
                    else:
                        extracted_librosa[stem_name] = str(raw_features)
                librosa_total_sec += time.perf_counter() - t_lib_start

                # B. PyTorch Tensor 特徴量抽出 (GPU cuFFT Wiener-Khinchin HNR/NAP & Bulk STFT)
                t_ten_start = time.perf_counter()
                with torch.no_grad():
                    y_tensor = torch.from_numpy(y_np)
                    stem_feats = extract_tensor_features(y_tensor, sr, device, spectro_path=spectro_path)
                    extracted_tensor[stem_name] = stem_feats
                tensor_total_sec += time.perf_counter() - t_ten_start

                # C. Essentia 特徴量抽出 (mix ステムのみ)
                if stem_name == "mix" and essentia_models:
                    t_ess_start = time.perf_counter()
                    patches = extract_mel_patches(y_np, sr, n_patches=64)
                    extracted_essentia = run_essentia_serialized(patches, essentia_models)
                    essentia_total_sec += time.perf_counter() - t_ess_start
            finally:
                # ADV-03: Tensor / Essentia 処理完了後に AudioContext を安全にクリア (use-after-free 防止)
                ctx.clear()

        finally:
            # Advisory 2: Windows 共有メモリハンドルをタスク毎に確実に解放 (1450防止)
            shm.close()

    total_sec = time.perf_counter() - t_start

    # 構造の整合化
    final_librosa = {"demucs": {}}
    for k, v in extracted_librosa.items():
        if k == "mix":
            final_librosa["mix"] = v
        else:
            final_librosa["demucs"][k] = v

    final_tensor = {"demucs": {}}
    for k, v in extracted_tensor.items():
        if k == "mix":
            final_tensor["mix"] = v
        else:
            final_tensor["demucs"][k] = v

    return {
        "status": "success",
        "librosa": final_librosa,
        "tensor": final_tensor,
        "essentia": extracted_essentia,
        "profile": {
            "extract_total_sec": total_sec,
            "librosa_sec": librosa_total_sec,
            "tensor_sec": tensor_total_sec,
            "essentia_sec": essentia_total_sec,
        }
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
