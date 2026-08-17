"""
worker_tensor.py
================
Goから起動される PyTorch (tensor) 依存のワーカーですわ。
CUDA 13 / CPU フォールバックによる高速なFFT・テンソル計算を担当し、
瞬時位相(Phase)、PSD、Spectral Flux、帯域別Envelope 等を抽出しますの。
"""

import argparse
import json
import logging
import os
import sys
import time
import torch

# プロジェクト内のモジュール
import shm_interop
from analyzer import extract_tensor_features

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [%(name)s] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def main():
    setup_logger()
    logger = logging.getLogger("TensorWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--shm-metadata", required=True, help="JSON string from DemucsWorker")
    parser.add_argument("--track-hash", required=True)
    args = parser.parse_args()

    # CPU/GPU 判定
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    logger.info(f"使用する演算デバイスを決定いたしましたわ: {device}")

    try:
        metadata = json.loads(args.shm_metadata)
        sr = metadata["sr"]
        stems_info = metadata["stems"]
    except Exception as e:
        logger.exception("メタデータのパースに失敗いたしましたわ！")
        sys.exit(1)

    t_start = time.perf_counter()
    extracted_features = {}

    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        spectro_path = info.get("spectro_path")
        
        logger.info(f"ステム [{stem_name}] の共有メモリ [{tag_name}] を処理しておりますわ")
        shm, y_np = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name)
        
        try:
            import warnings
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", UserWarning)
                # torch.from_numpy は Zero-copy でメモリをマッピングしますの
                y_tensor = torch.from_numpy(y_np)
            
            # 特徴量抽出
            stem_feats = extract_tensor_features(y_tensor, sr, device, spectro_path=spectro_path)
            extracted_features[stem_name] = stem_feats
            
        except Exception as e:
            logger.exception(f"ステム [{stem_name}] の処理中にエラーが発生いたしましたわ！")
            sys.exit(1)
        finally:
            shm.close()
            if device.type == "cuda":
                torch.cuda.empty_cache()
            import gc
            gc.collect()

    total_sec = time.perf_counter() - t_start
    logger.info(f"全ステムの PyTorch (Tensor) 特徴量抽出が無事に完了いたしましたわ (経過: {total_sec:.4f}s)")
    
    final_features = {"demucs": {}}
    for k, v in extracted_features.items():
        if k == "mix":
            final_features["mix"] = v
        else:
            final_features["demucs"][k] = v
            
    # 結果を出力
    print(json.dumps({
        "status": "success",
        "features": final_features,
        "profile": {
            "extract": total_sec,
            "total": total_sec
        }
    }))
    sys.exit(0)

if __name__ == "__main__":
    main()
