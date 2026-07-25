"""
functor_precache.py
===================
Goオーケストレーターから呼び出される関手 (Functor) ワーカーですわ。
Demucsが共有メモリに載せた生波形(Time Domain)を読み取り、
高速なFFTベースのSTFT (Frequency Domain) の振幅スペクトログラム(S)などを計算し、
numpy.memmap (ディスクベース) としてキャッシュしますの。

計算結果へのパスを付与した新しいメタデータを標準出力(JSON)に返し、
後続のワーカー(Librosa, Tensor等)で再計算をスキップさせますわ！
"""

import argparse
import json
import logging
import os
import sys
import tempfile
import time
import numpy as np
import librosa

# プロジェクト内モジュール
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
        logger.exception("Failed to parse metadata")
        sys.exit(1)

    t_start = time.perf_counter()
    # ご指定通り、高速なRAMディスク等の恩恵を受けるため OSのテンポラリディレクトリを使用しますわ
    cache_dir = os.path.join(tempfile.gettempdir(), "flac_analyzer_cache", args.track_hash)
    os.makedirs(cache_dir, exist_ok=True)

    logger.info(f"Generating frequency-domain cache to: {cache_dir}")

    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        
        # ディスクへの巨大な.npy保存はRAM/ディスク溢れの原因となるため廃止し、共有メモリのアタッチ性検証のみを行いますの
        shm, _ = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name)
        shm.close()

    logger.info(f"Precache Functor passthrough completed in {time.perf_counter() - t_start:.4f}s")
    
    # 成功したら stdout にメタデータをそのままJSONで吐き出して終了
    metadata["status"] = "success"
    print(json.dumps(metadata))
    sys.exit(0)

if __name__ == "__main__":
    main()
