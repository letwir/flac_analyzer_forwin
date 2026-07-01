"""
worker_demucs.py
================
Goから起動されるDemucs専用ワーカーですわ。
指定されたFLACファイルを読み込み、波形分離を行い、
Goが確保した共有メモリにZero-copyで書き込んだ後、
メタデータをJSONで標準出力して exit 0 しますの。
"""

import argparse
import json
import logging
import os
import sys
import time
import tomllib

# プロジェクト内のモジュール
import models
import shm_interop
import librosa

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [%(name)s] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)] # GoにパースさせるJSONと混ざらないようstderrに出力
    )

def main():
    setup_logger()
    logger = logging.getLogger("DemucsWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--flac-path", required=True, help="Target FLAC file path")
    parser.add_argument("--shm-tags", required=True, help="JSON string of stem to shm_tag mapping")
    parser.add_argument("--track-hash", required=False, default="dummy", help="MD5 hash of the track")
    parser.add_argument("--use-dml", action="store_true", help="Use DirectML")
    parser.add_argument("--start-sample", type=int, default=0)
    parser.add_argument("--end-sample", type=int, default=-1)
    args = parser.parse_args()

    try:
        shm_tags = json.loads(args.shm_tags)
    except Exception as e:
        logger.error(f"Failed to parse --shm-tags: {e}")
        sys.exit(1)

    logger.info(f"Loading FLAC: {args.flac_path}")
    try:
        from flac_decode import build_flac_handle, process_slice_with_seq_safety
        handle = build_flac_handle(args.flac_path)
        start_samp = args.start_sample
        end_samp = args.end_sample if args.end_sample != -1 else handle.total_samples
        
        y, md5_hash = process_slice_with_seq_safety(
            args.flac_path,
            start_samp,
            end_samp,
            handle.sample_rate,
            handle.channels
        )
        y = y.T # demucs_worker expects (channels, samples)
        sr = 44100
    except Exception as e:
        logger.error(f"Failed to load audio: {e}")
        sys.exit(1)

    logger.info("Initializing Demucs model...")
    models.init_global_demucs(use_dml=args.use_dml)

    logger.info("Starting separation...")
    t_start = time.perf_counter()
    stem_context = models.GLOBAL_DEMUCS.separate(y, sr)
    logger.info(f"Separation completed in {time.perf_counter() - t_start:.4f}s")

    # 書き込んだ共有メモリのmmapオブジェクトを保持するリスト（終了するまでGCさせないため）
    shm_objects = []
    
    # Goに渡すためのメタデータ
    metadata = {
        "status": "success",
        "audio_hash": md5_hash,
        "sr": sr,
        "stems": {}
    }

    logger.info("Writing to shared memory...")
    try:
        for stem_name, ctx in stem_context.stems.items():
            if stem_name not in shm_tags:
                logger.warning(f"No SHM tag provided for stem: {stem_name}")
                continue
            
            tag_name = shm_tags[stem_name]
            logger.info(f"Writing stem '{stem_name}' to {tag_name}")
            
            # Zero-copy write
            shm = shm_interop.write_to_shm(tag_name, ctx.y)
            shm_objects.append(shm)
            
            metadata["stems"][stem_name] = {
                "shm_tag": tag_name,
                "shape": ctx.y.shape,
                "dtype": str(ctx.y.dtype)
            }
            
    except Exception as e:
        logger.error(f"Failed to write to SHM: {e}")
        sys.exit(1)

    # 成功したら stdout にメタデータを吐き出して終了
    # GoはこのJSONを受け取って Freeze() を実行し、worker_librosa を起動しますわ。
    print(json.dumps(metadata))
    sys.exit(0)

if __name__ == "__main__":
    main()
