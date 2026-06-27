"""
Analyzer Worker module for FLAC Analyzer
========================================
マルチプロセス（子プロセス）上で各ステムの特徴量抽出を実行するワーカーですわ！
"""

import logging
import time
from typing import Any

import numpy as np

# analyzer モジュールから必要な定義をインポートしますの
from analyzer import (
    AudioContext,
    STEM_CONFIGS,
    librosa_extractor
)

def process_stem(stem_name: str, y: np.ndarray, sr: int) -> Any:
    """
    1つのステムに対する Pre-warming と Librosa特徴量抽出を実行し、
    結果のRawFeaturesオブジェクトを返しますわ！
    
    マルチプロセス環境下でシリアライズ（pickle）可能な軽量データのみを
    親プロセスへ転送することで、IPCのオーバーヘッドを最小化しますの。
    """
    import multiprocessing
    proc_name = multiprocessing.current_process().name

    t_start = time.perf_counter()

    # 1. AudioContext の構築
    ctx = AudioContext(y=y, sr=sr, source=stem_name)

    # 2. config の取得 (未定義ステムは 'other' の設定をフォールバック利用しますわ)
    config = STEM_CONFIGS.get(stem_name, STEM_CONFIGS["other"])
    
    # 3. Pre-warming の実行 (遅延プロパティへのアクセスでキャッシュを強制評価しますの)
    for prop in config["warmup"]:
        try:
            _ = getattr(ctx, prop)
        except Exception as e:
            logging.warning(
                f"[{proc_name}] [Worker] [{stem_name}] "
                f"Pre-warming プロパティ '{prop}' 評価エラー (続行しますわ): {e}"
            )
    
    t_warmup = time.perf_counter()

    # 4. Extractor の実行
    # config["extractor"] を利用して動的ディスパッチすることも可能ですわ（今回は共通して librosa_extractor を利用）
    raw_features = librosa_extractor.run(ctx)
    
    # 5. 計算後、子プロセス内の重い中間配列(NumPy)を明示的に解放しますわ！
    ctx.clear()
    
    t_extract = time.perf_counter()
    
    logging.info(
        f"[{proc_name}] [Worker] [{stem_name}] "
        f"Warmup完了: {t_warmup - t_start:.4f}s | "
        f"抽出完了: {t_extract - t_warmup:.4f}s"
    )

    return raw_features


def process_stem_shm(
    stem_name: str, shm_name: str, shape: tuple[int, ...], dtype_name: str, sr: int
) -> Any:
    """
    共有メモリ（SharedMemory）を介して波形データをアタッチし、
    特徴量抽出（Pre-warming と Extractor）を実行後、クローズしますわ！
    """
    import multiprocessing
    from multiprocessing.shared_memory import SharedMemory
    import numpy as np
    import time
    import logging

    proc_name = multiprocessing.current_process().name
    t_start = time.perf_counter()

    # 1. 共有メモリにアタッチ
    shm = SharedMemory(name=shm_name)
    try:
        # np.ndarray ビューを作成してデータを参照
        y = np.ndarray(shape, dtype=np.dtype(dtype_name), buffer=shm.buf)
        
        # 2. AudioContext の構築 (y のコピーを作らず参照)
        ctx = AudioContext(y=y, sr=sr, source=stem_name)

        # 3. config の取得
        config = STEM_CONFIGS.get(stem_name, STEM_CONFIGS["other"])
        
        # 4. Pre-warming の実行
        for prop in config["warmup"]:
            try:
                _ = getattr(ctx, prop)
            except Exception as e:
                logging.warning(
                    f"[{proc_name}] [WorkerSHM] [{stem_name}] "
                    f"Pre-warming プロパティ '{prop}' 評価エラー (続行しますわ): {e}"
                )
        
        t_warmup = time.perf_counter()

        # 5. Extractor の実行
        raw_features = librosa_extractor.run(ctx)
        
        # 6. 配列参照の明示的な解放
        ctx.clear()
        
    finally:
        # 7. 共有メモリを子プロセス側でクローズ (親側の unlink とは別ですわ)
        shm.close()

    t_extract = time.perf_counter()
    
    logging.info(
        f"[{proc_name}] [WorkerSHM] [{stem_name}] "
        f"Warmup完了: {t_warmup - t_start:.4f}s | "
        f"抽出完了: {t_extract - t_warmup:.4f}s"
    )

    return raw_features
