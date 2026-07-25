import os
import platform
import tempfile
import psutil
import json
import numpy as np
from multiprocessing import shared_memory
from analyzer import AudioContext, StemContext

_SHM_KEEP_ALIVE = {}  # hash_id -> list of SharedMemory

def clear_producer_shm_cache():
    """前回のループで作成された SharedMemory オブジェクトをクローズしキャッシュを空にしますわ"""
    global _SHM_KEEP_ALIVE
    for hash_id, shm_list in _SHM_KEEP_ALIVE.items():
        for shm in shm_list:
            try:
                shm.close()
            except Exception:
                pass
    _SHM_KEEP_ALIVE.clear()


def get_default_shm_dir() -> str:
    """Windows / Linux で互換性のある一時共有ディレクトリを取得しますわ"""
    if platform.system() == "Windows":
        path = os.path.join(tempfile.gettempdir(), "flac_analyzer_shm")
    else:
        path = "/dev/shm"
    os.makedirs(path, exist_ok=True)
    return path

def get_transfer_mode(
    filepath: str, 
    total_samples: int, 
    channels: int, 
    bits_per_sample: int,
    min_free_ram_gb: float = 8.0
) -> str:
    """システムの実行時空きRAM量と推定PCMサイズに基づいて、転送モードを動的に判定しますの"""
    # 1. デコード後 PCM の推定サイズ (bytes)
    bytes_per_sample = bits_per_sample // 8
    estimated_pcm_bytes = total_samples * channels * bytes_per_sample
    
    # 2. 分離処理時の中間データ等を含めた推定要求メモリ (PCMの約3倍と見積もる)
    estimated_required_ram = estimated_pcm_bytes * 3.0
    
    # 3. システムの現在の利用可能な空き RAM (bytes)
    available_ram = psutil.virtual_memory().available
    
    # 4. 処理実行後の残り空き RAM 容量の予測
    remaining_free_ram = available_ram - estimated_required_ram
    margin_bytes = min_free_ram_gb * 1024 * 1024 * 1024
    
    # 残り空きRAMが安全マージンを下回る場合、またはファイルサイズ自体が500MB超の場合はディスク退避 (disk)
    if remaining_free_ram < margin_bytes or os.path.getsize(filepath) > 500 * 1024 * 1024:
        return "disk"
        
    return "shm"

def save_stems(hash_id: str, stem_context: StemContext, mode: str, shm_dir: str) -> dict:
    """指定されたモードでステムデータを退避し、Queue用のメタデータを返しますわ"""
    global _SHM_KEEP_ALIVE
    transfer_meta = {

        "hash": hash_id,
        "shm_type": mode
    }
    
    if mode == "shm":
        # SharedMemory API 方式
        shm_names = {}
        shm_list = []
        for name, ctx in stem_context.stems.items():
            shm = shared_memory.SharedMemory(create=True, size=ctx.y.nbytes)
            shm_names[name] = shm.name
            shm_list.append(shm)
            shm_arr = np.ndarray(ctx.y.shape, dtype=ctx.y.dtype, buffer=shm.buf)
            shm_arr[:] = ctx.y[:]
            
        # 直近の shm オブジェクトを保持しますわ
        _SHM_KEEP_ALIVE[hash_id] = shm_list
        
        # FIFO で古いトラックの共有メモリハンドルを解放 (キュー最大サイズが32なので、64トラック分あれば確実に安全ですわ)
        MAX_CACHE_TRACKS = 64
        if len(_SHM_KEEP_ALIVE) > MAX_CACHE_TRACKS:
            oldest_hash = next(iter(_SHM_KEEP_ALIVE))
            old_shm_list = _SHM_KEEP_ALIVE.pop(oldest_hash)
            for old_shm in old_shm_list:
                try:
                    old_shm.close()
                except Exception:
                    pass
                    
        transfer_meta["shm_names"] = shm_names
        transfer_meta["shapes"] = {name: ctx.y.shape for name, ctx in stem_context.stems.items()}
        transfer_meta["dtypes"] = {name: str(ctx.y.dtype) for name, ctx in stem_context.stems.items()}
        transfer_meta["srs"] = {name: ctx.sr for name, ctx in stem_context.stems.items()}
        transfer_meta["sources"] = {name: ctx.source for name, ctx in stem_context.stems.items()}
    else:
        # .npy 一時ディスクキャッシュ方式
        path = os.path.join(shm_dir, f"demucs_{hash_id}")
        os.makedirs(path, exist_ok=True)
        params = {}
        for name, ctx in stem_context.stems.items():
            np.save(os.path.join(path, f"{name}.npy"), np.ascontiguousarray(ctx.y))
            params[name] = {"sr": ctx.sr, "source": ctx.source}
        with open(os.path.join(path, "params.json"), "w") as f:
            json.dump(params, f)
        transfer_meta["shm_path"] = path
        
    return transfer_meta

def load_stems(transfer_meta: dict, shm_dir: str) -> StemContext:
    """透過的ステムデータロード（同型性の保証）を行いますの"""
    mode = transfer_meta["shm_type"]
    stems = {}
    
    if mode == "shm":
        # SharedMemory API からの復元 (即コピーにより独立化)
        for name, shm_name in transfer_meta["shm_names"].items():
            shape = transfer_meta["shapes"][name]
            dtype = transfer_meta["dtypes"][name]
            sr = transfer_meta["srs"][name]
            source = transfer_meta["sources"][name]
            
            shm = shared_memory.SharedMemory(name=shm_name)
            shm_arr = np.ndarray(shape, dtype=dtype, buffer=shm.buf)
            y = shm_arr.copy()
            shm.close()
            stems[name] = AudioContext(y, sr, source)
    else:
        # .npy 一時ディスクキャッシュからの復元
        path = transfer_meta["shm_path"]
        with open(os.path.join(path, "params.json")) as f:
            params = json.load(f)
        for name in params:
            y = np.load(os.path.join(path, f"{name}.npy"))
            stems[name] = AudioContext(y, params[name]["sr"], params[name]["source"])
            
    return StemContext(stems=stems)

def cleanup_stems(transfer_meta: dict, shm_dir: str):
    """リソースのセーフクリーンアップを実行しますわ"""
    mode = transfer_meta["shm_type"]
    if mode == "shm":
        for shm_name in transfer_meta["shm_names"].values():
            try:
                shm = shared_memory.SharedMemory(name=shm_name)
                shm.close()
                shm.unlink()
            except Exception:
                pass
    else:
        import shutil
        path = transfer_meta["shm_path"]
        if os.path.exists(path):
            try:
                shutil.rmtree(path)
            except Exception:
                pass
