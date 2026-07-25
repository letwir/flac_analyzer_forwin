"""
shm_interop.py
================
Windows Named Shared Memory Interoperability Layer.
Go (orchestrator) と Python 間での Zero-copy 共有メモリパイプラインを実現しますわ！
"""

import mmap
import numpy as np

def write_to_shm(name: str, y: np.ndarray) -> mmap.mmap:
    """
    Goが確保した共有メモリーに波形データを Zero-copy で書き込みますの。
    name: 共有メモリーのタグ名 (例: "Local\\FlacShm_mix")
    y: 書き込む numpy配列
    戻り値: 保持すべき mmap オブジェクト (破棄されると Windows ではデータが消滅しますわ！)
    """
    size = y.nbytes
    # Windowsのページングファイルバック共有メモリーを開く/作成するには fd=-1 を指定しますわ
    shm = mmap.mmap(-1, size, tagname=name, access=mmap.ACCESS_WRITE)
    # 巨大な bytes オブジェクトのコピーを避けるため、ndarray ビュー経由でコピーしますの
    shm_arr = np.ndarray(y.shape, dtype=y.dtype, buffer=shm)
    np.copyto(shm_arr, y)
    
    # mmapオブジェクトを返し、親プロセスでハンドルを保持し続けますわ
    return shm

def attach_shm_read_only(name: str, shape: tuple[int, ...], dtype_name: str) -> tuple[mmap.mmap, np.ndarray]:
    """
    Goが確保した共有メモリーを Read-Only で開き、mmapオブジェクトと numpy.ndarray ビューを返しますわ！
    ※Zero-copy を維持するため、利用後は必ず mmap.close() を呼び出して解放してくださいませ。
    """
    dtype = np.dtype(dtype_name)
    size = int(np.prod(shape) * dtype.itemsize)
    shm = mmap.mmap(-1, size, tagname=name, access=mmap.ACCESS_READ)
    
    # buffer=shm を指定することで、コピーなしの Zero-copy 参照を作りますの！
    arr = np.ndarray(shape, dtype=dtype, buffer=shm)
    return shm, arr
