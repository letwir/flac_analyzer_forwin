# Goが確保した共有メモリ（WORM）を用いた Python Zero-copy パイプラインの実装計画

前回の `shm_windows.go` 実装に続き、Python側で Demucs の分離波形を Go が確保した共有メモリ（Shared Memory）へ書き込み、Librosa がそれを Read-Only（Zero-copy）で参照するパイプラインを実装いたしますわ！

## 概要

現在 `pipeline.py` では Python 組み込みの `multiprocessing.shared_memory` を用いて親プロセスで共有メモリを確保し、子プロセスへ渡しています。
これを「Go 側で確保し、名前（tagname）のみを Python に渡す」あるいは「Python側からWindows名前付き共有メモリ（`Local\xxx`）として直接アクセスする」形に書き換えますの。
今回は **Python側の Zero-copy 読み書きインターフェース** および **パイプラインの結合部分** を実装します。

## 変更内容

### 1. `shm_interop.py` (新規作成)
Go 側で確保した Windows の名前付き共有メモリ（`Local\xxx`）にアクセスするためのラッパーモジュールです。
- `mmap` モジュールを使用し、`-1` をファイルディスクリプタとして渡すことで Windows の名前付き共有メモリにアクセスします。
- `write_to_shm(name, array)`: Demucsの `ndarray` を共有メモリに書き込みます (`ACCESS_WRITE`)。
- `read_from_shm(name, shape, dtype)`: Read-Only (`mmap.ACCESS_READ`) で共有メモリを `ndarray` として Zero-copy 参照します。

### 2. `analyzer_worker.py` (一部修正)
- `process_stem_shm` 関数を修正し、`multiprocessing.shared_memory` ではなく `shm_interop` の `read_from_shm` を用いて Read-Only でアタッチするように変更します。これにより、Go 側で `Freeze()` (PAGE_READONLY) された後でも安全に参照できます。

### 3. `pipeline.py` (一部修正)
- `analyze_segment_pipeline` 内の共有メモリ確保ロジック (`SharedMemory(create=True)`) を廃止します。
- Python側でGo連携の準備として、一旦フォールバック用のWindows名付き共有メモリ確保ロジックを入れるか、または `shm_interop` を経由して書き込む形にリファクタリングします。
