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
        logger.error(f"Failed to parse metadata: {e}")
        sys.exit(1)

    t_start = time.perf_counter()
    
    # OSのテンポラリディレクトリ(Q:ドライブ等)が容量不足になるのを防ぐため、プロジェクト直下の .cache を使いますわ
    base_cache_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".cache")
    cache_dir = os.path.join(base_cache_dir, args.track_hash)
    os.makedirs(cache_dir, exist_ok=True)

    logger.info(f"Generating frequency-domain cache to: {cache_dir}")

    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        
        shm, y_np = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name)
        
        try:
            # 1. 振幅スペクトログラム(S)の計算: AudioContext.spectro 互換 (n_fft=2048, hop_length=512)
            # librosa.stft を使用して Librosa ワーカーとの完全互換性を確保
            S_complex = librosa.stft(y_np, n_fft=2048, hop_length=512)
            S_mag = np.abs(S_complex)
            
            # ディスクへ保存 (.npy形式が確実かつ高速)
            S_mag_path = os.path.join(cache_dir, f"{stem_name}_S_mag.npy")
            np.save(S_mag_path, S_mag)
            
            # メタデータにパスを書き込む
            metadata["stems"][stem_name]["spectro_path"] = S_mag_path
            
            logger.info(f"Cached spectro for {stem_name}: {S_mag.shape}")
        except Exception as e:
            logger.error(f"Error calculating precache for {stem_name}: {e}")
            sys.exit(1)
        finally:
            shm.close()

    logger.info(f"Precache Functor completed in {time.perf_counter() - t_start:.4f}s")
    
    # 成功したら stdout に新しいメタデータをJSONで吐き出して終了
    metadata["status"] = "success"
    print(json.dumps(metadata))
    sys.exit(0)

if __name__ == "__main__":
    main()
