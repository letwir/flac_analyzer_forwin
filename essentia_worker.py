"""
essentia_worker.py
=================
Goから起動されるEssentia専用ワーカーですわ。
Demucsから書き込まれた共有メモリ（mixステム）をアタッチし、
Essentia(EffNet)による特徴量抽出を行って結果をJSONで出力しますの。
"""

import argparse
import json
import logging
import sys
import time

# プロジェクト内のモジュール
import shm_interop
import models

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [%(name)s] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)] # GoにパースさせるJSONと混ざらないようstderrに出力
    )

def main():
    setup_logger()
    logger = logging.getLogger("EssentiaWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--shm-metadata", required=True, help="JSON string containing sr and stems metadata from DemucsWorker")
    parser.add_argument("--track-hash", required=True, help="MD5 hash of the track for DB primary key")
    args = parser.parse_args()

    try:
        metadata = json.loads(args.shm_metadata)
        sr = metadata["sr"]
        stems_info = metadata["stems"]
    except Exception as e:
        logger.error(f"Failed to parse --shm-metadata: {e}")
        sys.exit(1)
        
    if "mix" not in stems_info:
        logger.error("Missing 'mix' stem in SHM metadata. Essentia requires the mix stem.")
        sys.exit(1)

    logger.info("Initializing Essentia models...")
    models.init_global_essentia()

    logger.info("Starting Essentia extraction from shared memory (mix)...")
    t_start = time.perf_counter()
    
    stem_info = stems_info["mix"]
    shm_tag = stem_info["shm_tag"]
    shape = tuple(stem_info["shape"])
    dtype = stem_info["dtype"]
    
    # 共有メモリから zero-copy read
    shm, y = shm_interop.attach_shm_read_only(shm_tag, shape, dtype)
    
    predictions = {}
    try:
        # Essentia の実行
        predictions = models.GLOBAL_ESSENTIA.run_all(y, args.track_hash)
    except Exception as e:
        logger.error(f"Essentia extraction failed: {e}")
        shm.close()
        sys.exit(1)
        
    shm.close()
    
    t_end = time.perf_counter()
    logger.info(f"Essentia extraction completed in {t_end - t_start:.4f}s")
    
    # 結果を標準出力へ JSON として吐き出しますの
    output = {
        "status": "success",
        "predictions": predictions
    }
    print(json.dumps(output))
    sys.exit(0)

if __name__ == "__main__":
    main()
