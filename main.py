"""
Main Entrypoint for FLAC Analyzer - Demucs Full-Throttle Pipeline
==================================================================
Producer-Consumer 並列パイプラインで Demucs + Librosa + Essentia を
全 FLAC ファイルにぶん回しますわ。出力先は PostgreSQL 直ですの。
"""

import argparse
import glob
import logging
import multiprocessing
import os
import sys
import tempfile
import time
from multiprocessing import sharedctypes

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
    """圏論的役割別 24bit ANSI カラーフォーマッタですわ。
    タグ種別で色相を、プロセス階層で彩度を変化させますの。"""

    fmt = "%(asctime)s [%(levelname)s] %(message)s"
    RESET = "\033[0m"

    # ──────────────────────────────────────────────
    # タグ → (hue°, saturation, lightness)
    # ──────────────────────────────────────────────
    # [Morphism] サブタグは青系で色相を段階的にずらしますわ
    _TAG_HSL: dict[str, tuple[float, float, float]] = {
        # --- Morphism サブタグ (青系グラデーション) ---
        "[SHM]": (180.0, 0.80, 0.58),  # シアン
        "[Demucs]": (195.0, 0.80, 0.58),  # シアン青
        "[Librosa]": (213.0, 0.80, 0.58),  # 青
        "[Morphism]": (210.0, 0.80, 0.58),  # 青 (fallback)
        "[Essentia]": (228.0, 0.78, 0.60),  # 青紫
        # --- プロセスライフサイクル ---
        "[Initial Object]": (120.0, 0.80, 0.55),  # 緑
        "[Terminal Object]": (0.0, 0.80, 0.55),  # 赤
        # --- 自己射 ---
        "[Endomorphism]": (270.0, 0.72, 0.62),  # 紫
        # --- 副作用モナド ---
        "[IO Monad]": (45.0, 0.88, 0.58),  # 黄
        "[Effect]": (28.0, 0.88, 0.58),  # 橙
    }

    # 優先順位: Morphism サブタグを先に評価しますわ
    _TAG_PRIORITY: tuple[str, ...] = (
        "[SHM]",
        "[Demucs]",
        "[Librosa]",
        "[Essentia]",  # Morphism サブタグ (高優先)
        "[Initial Object]",
        "[Terminal Object]",
        "[Morphism]",  # Morphism fallback
        "[Endomorphism]",
        "[IO Monad]",
        "[Effect]",
    )

    # デフォルト: 明るい灰色 (彩度ゼロ)
    _DEFAULT_HSL: tuple[float, float, float] = (0.0, 0.0, 0.78)

    # WARNING / ERROR / CRITICAL の上書き色
    _WARN_HSL: tuple[float, float, float] = (48.0, 1.00, 0.62)
    _ERROR_HSL: tuple[float, float, float] = (0.0, 0.95, 0.58)
    _CRIT_HSL: tuple[float, float, float] = (0.0, 1.00, 0.45)

    # ──────────────────────────────────────────────
    # プロセス名 → 彩度乗数 (親=1.0、子=0.70)
    # ──────────────────────────────────────────────
    _PROC_SAT: dict[str, float] = {
        "main": 1.00,
        "MainProcess": 1.00,
        "Producer": 0.70,
    }
    _CONSUMER_SAT: float = 0.70
    _UNKNOWN_SAT: float = 0.55

    def __init__(self, use_color: bool = True):
        super().__init__()
        self.use_color = use_color

    # ──────────────────────────────────────────────
    # 内部ユーティリティ
    # ──────────────────────────────────────────────
    @staticmethod
    def _hsl_to_ansi(h: float, s: float, l: float) -> str:
        """HSL (h:0-360, s:0-1, l:0-1) → 24bit 前景色 ANSI エスケープ"""
        import colorsys

        r, g, b = colorsys.hls_to_rgb(h / 360.0, l, s)
        return f"\033[38;2;{int(r * 255)};{int(g * 255)};{int(b * 255)}m"

    def _pick_hsl(self, record: logging.LogRecord) -> tuple[float, float, float]:
        """レコードの levelno とメッセージタグから HSL を決定しますわ"""
        if record.levelno >= logging.CRITICAL:
            return self._CRIT_HSL
        if record.levelno >= logging.ERROR:
            return self._ERROR_HSL
        if record.levelno >= logging.WARNING:
            return self._WARN_HSL

        msg = record.getMessage()
        for tag in self._TAG_PRIORITY:
            if tag in msg:
                return self._TAG_HSL[tag]
        return self._DEFAULT_HSL

    def _sat_mult(self, record: logging.LogRecord) -> float:
        """プロセス名から彩度乗数を返しますわ"""
        proc = record.processName or ""
        if proc in self._PROC_SAT:
            return self._PROC_SAT[proc]
        if proc.startswith("Consumer"):
            return self._CONSUMER_SAT
        return self._UNKNOWN_SAT

    def format(self, record: logging.LogRecord) -> str:
        base = logging.Formatter(self.fmt).format(record)
        if not self.use_color:
            return base

        h, s, l = self._pick_hsl(record)
        s_adj = min(1.0, s * self._sat_mult(record))
        return self._hsl_to_ansi(h, s_adj, l) + base + self.RESET


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
    p = argparse.ArgumentParser(description="FLAC Analyzer - Demucs Full-Throttle (Single File)")
    p.add_argument(
        "filepath", help="解析対象の単一 FLAC ファイルパス"
    )
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
    print("  FLAC Analyzer - Demucs Full-Throttle (Single File Mode)")
    print(f"  Target: {args.filepath}")
    print("=" * 60)

    if not os.path.exists(args.filepath):
        logging.error(f"指定されたファイルが見つかりませんわ: {args.filepath}")
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

