"""
Main Entrypoint for FLAC Analyzer - Demucs Full-Throttle Pipeline
==================================================================
Producer-Consumer 並列パイプラインで Demucs + Librosa + Essentia を
全 FLAC ファイルにぶん回しますわ。出力先は PostgreSQL 直ですの。
"""

import argparse
import logging
import os
import sys

os.environ["PYTHONUTF8"] = "1"
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["OMP_NUM_THREADS"] = "1"
os.environ["OPENBLAS_NUM_THREADS"] = "1"
os.environ["MKL_NUM_THREADS"] = "1"
os.environ["NUMEXPR_NUM_THREADS"] = "1"
os.environ["VECLIB_MAXIMUM_THREADS"] = "1"
os.environ["INGESTER_DATABASE_URL"] = (
    "postgres://ingester:ingester_8852@db.tigris-tailor.ts.net:5432/db"
)


class ColorFormatter(logging.Formatter):
    """圏論的役割別 ANSI 8色フォーマッタですわ。
    黄・赤・橙は WARN/ERROR 専用とし、正常進行は暗→明のグラデーションで視認性を確保しますの。"""

    fmt = "%(asctime)s [%(levelname)s] %(message)s"
    RESET = "\033[0m"

    # ──────────────────────────────────────────────
    # タグ → ANSI 8色 (黄・赤・橙は完全排除)
    # 暗 (Level 1: Dim Gray) → 明 (Level 6: Bold Bright White)
    # ──────────────────────────────────────────────
    _TAG_ANSI: dict[str, str] = {
        "[Initial Object]": "\033[2;37m",   # Level 1: Dim Gray (最暗)
        "[HASH]": "\033[2;37m",             # Level 1: Dim Gray
        "[SHM]": "\033[34m",                # Level 2: Blue (暗め)
        "[Demucs]": "\033[35m",             # Level 3: Magenta (中暗)
        "[Librosa]": "\033[36m",            # Level 4: Cyan (中明)
        "[Essentia]": "\033[36m",           # Level 4: Cyan
        "[Morphism]": "\033[36m",           # Level 4: Cyan (fallback)
        "[IO Monad]": "\033[32m",           # Level 5: Green (明)
        "[Effect]": "\033[32m",             # Level 5: Green
        "[Terminal Object]": "\033[1;97m", # Level 6: Bold Bright White (最光/完成)
        "[TAG]": "\033[1;97m",              # Level 6: Bold Bright White
    }

    _TAG_PRIORITY: tuple[str, ...] = (
        "[SHM]",
        "[Demucs]",
        "[Librosa]",
        "[Essentia]",
        "[IO Monad]",
        "[Effect]",
        "[TAG]",
        "[Initial Object]",
        "[Terminal Object]",
        "[HASH]",
        "[Morphism]",
    )

    # デフォルト
    _DEFAULT_ANSI: str = "\033[37m"

    # WARNING / ERROR / CRITICAL 専用色 (黄 / 赤)
    _WARN_ANSI: str = "\033[1;33m"   # Bold Yellow (WARN専用)
    _ERROR_ANSI: str = "\033[1;31m"  # Bold Red (ERROR専用)
    _CRIT_ANSI: str = "\033[1;31m"   # Bold Red

    def __init__(self, use_color: bool = True):
        super().__init__()
        self.use_color = use_color

    def _pick_ansi(self, record: logging.LogRecord) -> str:
        """レコードの levelno とメッセージタグから ANSI コードを決定しますわ"""
        if record.levelno >= logging.CRITICAL:
            return self._CRIT_ANSI
        if record.levelno >= logging.ERROR:
            return self._ERROR_ANSI
        if record.levelno >= logging.WARNING:
            return self._WARN_ANSI

        msg = record.getMessage()
        for tag in self._TAG_PRIORITY:
            if tag in msg:
                return self._TAG_ANSI[tag]
        return self._DEFAULT_ANSI

    def format(self, record: logging.LogRecord) -> str:
        base = logging.Formatter(self.fmt).format(record)
        if not self.use_color:
            return base

        ansi = self._pick_ansi(record)
        return ansi + base + self.RESET


def setup_logging(log_file_path: str = None):
    # Windows環境下での仮想端末処理（ANSIエスケープ）の有効化
    if sys.platform == "win32":
        try:
            import ctypes

            kernel32 = ctypes.windll.kernel32
            # 0x0004: ENABLE_VIRTUAL_TERMINAL_PROCESSING
            # STD_OUTPUT_HANDLE = -11
            stdout_handle = kernel32.GetStdHandle(-11)
            mode = ctypes.c_ulong()
            if kernel32.GetConsoleMode(stdout_handle, ctypes.byref(mode)):
                kernel32.SetConsoleMode(stdout_handle, mode.value | 0x0004)
        except Exception:
            pass

    root_logger = logging.getLogger()
    root_logger.setLevel(logging.INFO)

    # 既存のハンドラをすべてクリアしますわ
    for handler in list(root_logger.handlers):
        root_logger.removeHandler(handler)

    # コンソール出力 (カラー)
    use_color = sys.stdout.isatty()
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(logging.INFO)
    console_handler.setFormatter(ColorFormatter(use_color=use_color))
    root_logger.addHandler(console_handler)

    # ファイル出力 (プレーン)
    if log_file_path:
        file_handler = logging.FileHandler(log_file_path, encoding="utf-8")
        file_handler.setLevel(logging.INFO)
        file_formatter = logging.Formatter("%(asctime)s [%(levelname)s] %(message)s")
        file_handler.setFormatter(file_formatter)
        root_logger.addHandler(file_handler)

    # 外部モジュールのログレベル制御ですわ
    logging.getLogger("numba").setLevel(logging.WARNING)
    logging.getLogger("llvmlite").setLevel(logging.WARNING)
    logging.getLogger("onnxruntime").setLevel(logging.WARNING)


# 初期設定としての仮ロギング（setup_loggingが呼ばれるまでのフォールバック）
setup_logging()


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="FLAC Analyzer - Demucs Full-Throttle (Single File)"
    )
    p.add_argument("filepath", help="解析対象の単一 FLAC ファイルパス")
    p.add_argument(
        "--workers",
        type=int,
        default=4,
        help="Librosa スレッドプール並列数 (デフォルト: 4)",
    )
    p.add_argument(
        "--dml", action="store_true", help="波形分離で DirectML (GPU) を有効化しますわ"
    )
    p.add_argument(
        "--models-dir",
        default="./models",
        help="モデルディレクトリ（デフォルト: ./models）",
    )
    return p.parse_args()


def main():
    args = parse_args()

    # 解析対象のファイルからログファイル名を自動生成し、ロギングを再設定しますわ
    if args.filepath:
        file_abs = os.path.abspath(args.filepath)
        dir_abs = os.path.dirname(file_abs)
        genre_sub_name = os.path.basename(dir_abs)
        genre_main_name = os.path.basename(os.path.dirname(dir_abs))
        log_file_name = f"log_{genre_main_name}__{genre_sub_name}.log"

        project_root = os.path.dirname(os.path.abspath(__file__))
        log_file_path = os.path.join(project_root, log_file_name)
        setup_logging(log_file_path)

    print("=" * 60)
    print("  🌹 FLAC Analyzer - Demucs フルアクセル（単一ファイル直接解析モード）")
    print(f"  ターゲット: {args.filepath}")
    print("=" * 60)

    if not os.path.exists(args.filepath):
        logging.error(f"指定されたファイルが存在いたしませんわ: {args.filepath}")
        sys.exit(1)

    import models
    from pipeline import process_single_flac_file_directly

    essentia_models = models.init_worker_onnx(args.models_dir)

    result = process_single_flac_file_directly(
        filepath=args.filepath,
        essentia_models=essentia_models,
        use_dml=args.dml,
    )
    logging.info(result)


if __name__ == "__main__":
    main()
