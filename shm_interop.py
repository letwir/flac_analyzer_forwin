import ctypes
import mmap
import sys
import numpy as np

_kernel32 = None
if sys.platform == "win32":
    try:
        _kernel32 = ctypes.windll.kernel32
    except Exception:
        _kernel32 = None

def pin_shm_memory(shm: mmap.mmap, size: int = 0) -> bool:
    """
    Win32 VirtualLock API を呼び出して、Python プロセス側でも共有メモリ領域を物理 RAM にピン留めしますわ！
    Go オーケストレーター側ですでに常駐化されていますが、本プロセスでのスワップアウトも二重に防止しますの。
    """
    if _kernel32 is None or shm is None:
        return False
    try:
        lock_size = size if size > 0 else shm.size()
        # mmap バッファのメモリアドレスを取得
        buf_from_mem = ctypes.c_char.from_buffer(shm)
        addr = ctypes.addressof(buf_from_mem)
        ret = _kernel32.VirtualLock(ctypes.c_void_p(addr), ctypes.c_size_t(lock_size))
        return bool(ret != 0)
    except Exception:
        return False

def unpin_shm_memory(shm: mmap.mmap, size: int = 0) -> bool:
    """
    Win32 VirtualUnlock API を呼び出して、物理 RAM のピン留めを解除しますわ！
    """
    if _kernel32 is None or shm is None:
        return False
    try:
        lock_size = size if size > 0 else shm.size()
        buf_from_mem = ctypes.c_char.from_buffer(shm)
        addr = ctypes.addressof(buf_from_mem)
        ret = _kernel32.VirtualUnlock(ctypes.c_void_p(addr), ctypes.c_size_t(lock_size))
        return bool(ret != 0)
    except Exception:
        return False

def estimate_shm_size(file_size: int, ratio: float = 3.5) -> int:
    """
    Go (shm_utils.go) の EstimateShmSize と同一の共有メモリ割り当てサイズを計算しますわ！
    """
    estimated = int(file_size * ratio)
    if estimated < 1024 * 1024:
        estimated = 1024 * 1024
    return estimated

def write_to_shm(name: str, y: np.ndarray, file_size: int = 0, estimated_size: int = 0, pin_memory: bool = False) -> mmap.mmap:
    """
    Goが確保した共有メモリーに波形データを Zero-copy で書き込みますの。
    name: 共有メモリーのタグ名 (例: "Local\\FlacShm_mix")
    y: 書き込む numpy配列
    file_size / estimated_size: Go側で CreateFileMapping されたセクションのサイズ
    pin_memory: Python 側でも VirtualLock を試行するかどうか
    戻り値: 保持すべき mmap オブジェクト
    """
    needed_size = y.nbytes

    # Windows上で既存の名前付き共有メモリを開く場合、
    # 0 を指定すると Go 側 (CreateFileMappingW) が作成した既存セクション全体サイズで安全にマッピングされますわ！
    # 失敗時のフォールバックとして needed_size を指定します。
    try:
        shm = mmap.mmap(-1, 0, tagname=name, access=mmap.ACCESS_WRITE)
    except Exception:
        shm = mmap.mmap(-1, needed_size, tagname=name, access=mmap.ACCESS_WRITE)

    # 巨大な bytes オブジェクトのコピーを避けるため、ndarray ビュー経由でコピーしますの
    shm_arr = np.ndarray(y.shape, dtype=y.dtype, buffer=shm)
    np.copyto(shm_arr, y)
    
    if pin_memory:
        pin_shm_memory(shm, needed_size)

    # mmapオブジェクトを返し、親プロセスでハンドルを保持し続けますわ
    return shm

def attach_shm_read_only(name: str, shape: tuple[int, ...], dtype_name: str, file_size: int = 0, estimated_size: int = 0, pin_memory: bool = False) -> tuple[mmap.mmap, np.ndarray]:
    """
    Goが確保した共有メモリーを Read-Only で開き、mmapオブジェクトと numpy.ndarray ビューを返しますわ！
    ※Zero-copy を維持するため、利用後は必ず mmap.close() を呼び出して解放してくださいませ。
    """
    dtype = np.dtype(dtype_name)
    needed_size = int(np.prod(shape) * dtype.itemsize)
    
    # 既存の共有メモリハンドルに対して安全にマッピングを開きます
    try:
        shm = mmap.mmap(-1, 0, tagname=name, access=mmap.ACCESS_READ)
    except Exception:
        map_size = estimated_size if estimated_size >= needed_size else needed_size
        shm = mmap.mmap(-1, map_size, tagname=name, access=mmap.ACCESS_READ)
    
    if pin_memory:
        pin_shm_memory(shm, needed_size)

    # buffer=shm を指定することで、コピーなしの Zero-copy 参照を作りますの！
    arr = np.ndarray(shape, dtype=dtype, buffer=shm)
    return shm, arr

