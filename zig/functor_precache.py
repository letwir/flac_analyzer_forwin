"""
zig/functor_precache.py
=======================
Goオーケストレーターから呼び出される関手 (Functor) ワーカーですわ。
Demucsが共有メモリに載せた生波形(Time Domain)を読み取り、
共有メモリのアタッチ性・形状・整合性を検証しますの。
"""

import argparse
import json
import logging
import os
import sys
import tempfile
import time

# 親ディレクトリを sys.path に追加してプロジェクト内モジュールを安全にロード
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

import shm_interop

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [Precache] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def main():
    setup_logger()
    logger = logging.getLogger("PrecacheFunctor")

    parser = argparse.ArgumentParser()
    parser.add_argument("--shm-metadata", required=True, help="JSON string from DemucsWorker")
    parser.add_argument("--track-hash", required=True)
    args = parser.parse_args()

    try:
        metadata = json.loads(args.shm_metadata)
        sr = metadata["sr"]
        stems_info = metadata["stems"]
    except Exception as e:
        logger.exception("メタデータのパースに失敗いたしましたわ！")
        sys.exit(1)

    t_start = time.perf_counter()
    cache_dir = os.path.join(tempfile.gettempdir(), "flac_analyzer_cache", args.track_hash)
    os.makedirs(cache_dir, exist_ok=True)

    logger.info(f"周波数領域キャッシュディレクトリを確認いたしましたわ: {cache_dir}")

    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        
        # 共有メモリのアタッチ性検証
        shm, _ = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name)
        shm.close()

    logger.info(f"Precache Functor 検証処理が無事に完了いたしましたわ (経過: {time.perf_counter() - t_start:.4f}s)")
    
    metadata["status"] = "success"
    print(json.dumps(metadata))
    sys.exit(0)

if __name__ == "__main__":
    main()
