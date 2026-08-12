"""
flac_tagger.py
==============
解析結果 (Librosa, Essentia, Tensor JSON) から FLAC VorbisComment タグ辞書を統合生成し、
アトミック書き込みおよび Windows タイムスタンプ保護（mtime, atime, ctime）を行って
FLAC 本体へ安全に焼き込むモジュールですわ！
ファイル使用中（ロック）の場合のバックオフリトライ機構を完備しておりますの。
"""

import argparse
import ctypes
from ctypes import wintypes
import json
import logging
import os
import re
import shutil
import sys
import tempfile
import time
import tomllib
from typing import Any
from mutagen.flac import FLAC


def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [FlacTagger] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def load_config() -> dict:
    config_path = os.path.join(os.path.dirname(__file__), "config.toml")
    if os.path.exists(config_path):
        try:
            with open(config_path, "rb") as f:
                return tomllib.load(f)
        except Exception as e:
            logging.warning(f"config.toml の読込中に警告が発生いたしましたわ: {e}")
    return {}

def set_win_timestamps(filepath: str, atime: float, mtime: float, ctime: float | None = None):
    """Windows タイムスタンプ（作成日時 ctime, 更新日時 mtime, アクセス日時 atime）を保護・復元いたしますの。"""
    try:
        os.utime(filepath, (atime, mtime))
    except Exception as e:
        logging.warning(f"utime 復元中に失敗いたしましたわ: {e}")

    if sys.platform == "win32" and ctime is not None:
        try:
            FILE_WRITE_ATTRIBUTES = 0x0100
            FILE_SHARE_READ = 0x00000001
            FILE_SHARE_WRITE = 0x00000002
            FILE_SHARE_DELETE = 0x00000004
            OPEN_EXISTING = 3
            FILE_FLAG_BACKUP_SEMANTICS = 0x02000000

            handle = ctypes.windll.kernel32.CreateFileW(
                filepath,
                FILE_WRITE_ATTRIBUTES,
                FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                None,
                OPEN_EXISTING,
                FILE_FLAG_BACKUP_SEMANTICS,
                None
            )
            if handle != -1 and handle != 0:
                c_time_hnsec = int((ctime + 11644473600) * 10000000)
                ft_low = c_time_hnsec & 0xFFFFFFFF
                ft_high = (c_time_hnsec >> 32) & 0xFFFFFFFF
                
                class FILETIME(ctypes.Structure):
                    _fields_ = [("dwLowDateTime", wintypes.DWORD), ("dwHighDateTime", wintypes.DWORD)]
                
                ft_create = FILETIME(ft_low, ft_high)
                ctypes.windll.kernel32.SetFileTime(handle, ctypes.byref(ft_create), None, None)
                ctypes.windll.kernel32.CloseHandle(handle)
        except Exception as e:
            logging.warning(f"Windows ctime (CreationTime) 復元中に失敗いたしましたわ: {e}")

def _safe_int(val: Any, scale: float = 1.0) -> int:
    try:
        v = float(val)
        if abs(v) < 1e-12:
            return 0
        return int(round(v * scale))
    except (ValueError, TypeError):
        return 0

def _safe_float_str(val: Any) -> str:
    try:
        v = float(val)
        return str(v)
    except (ValueError, TypeError):
        return "0.0"

MODEL_KEYS = [
    "APPROACHABILITY_3C", "DANCEABILITY", "ENGAGEMENT_3C", "FS_LOOP_DS",
    "GENDER", "GENRE_DISCOGS400", "GENRE_DORTMUND", "GENRE_ELECTRONIC",
    "GENRE_ROSAMERICA", "GENRE_TZANETAKIS", "MOODS_MIREX", "MOOD_ACOUSTIC",
    "MOOD_AGGRESSIVE", "MOOD_ELECTRONIC", "MOOD_HAPPY", "MOOD_PARTY",
    "MOOD_RELAXED", "MOOD_SAD", "VOICE_INSTRUMENTAL"
]

def build_flac_tags(librosa_data: dict, essentia_data: dict, tensor_data: dict, prefix: str = "") -> dict[str, str]:
    p = f"{prefix}_" if prefix else ""
    tags: dict[str, str] = {}

    # 1. Librosa 特徴量
    scalars = librosa_data.get("scalars", {})
    if not scalars and "features" in librosa_data:
        scalars = librosa_data["features"].get("scalars", {})

    if scalars:
        if "bpm" in scalars:
            tags[f"{p}LIBROSA_BPM"] = str(_safe_int(scalars["bpm"]))
        if "beat_regularity" in scalars:
            tags[f"{p}LIBROSA_BEAT_REGULARITY"] = str(_safe_int(scalars["beat_regularity"]))
        if "dominant_pitch" in scalars:
            tags[f"{p}LIBROSA_DOMINANT_PITCH"] = str(scalars["dominant_pitch"])
        
        # 浮動小数点数 (raw float) 指標
        if "zcr_mean" in scalars:
            tags[f"{p}LIBROSA_ZCR"] = _safe_float_str(scalars["zcr_mean"])
        elif "zcr" in scalars:
            tags[f"{p}LIBROSA_ZCR"] = _safe_float_str(scalars["zcr"])
            
        if "snr" in scalars and scalars["snr"] is not None:
            tags[f"{p}LIBROSA_SNR"] = _safe_float_str(scalars["snr"])
        if "rolloff_mean" in scalars:
            tags[f"{p}LIBROSA_ROLLOFF"] = _safe_float_str(scalars["rolloff_mean"])
        if "hnr" in scalars:
            tags[f"{p}LIBROSA_HNR"] = _safe_float_str(scalars["hnr"])

        # 整数・スケール整数指標
        if "spectral_bandwidth" in scalars:
            tags[f"{p}LIBROSA_SPECTRAL_BANDWIDTH"] = str(_safe_int(scalars["spectral_bandwidth"]))
        if "centroid_mean" in scalars:
            tags[f"{p}LIBROSA_SPECTRAL_CENTROID_MEAN"] = str(_safe_int(scalars["centroid_mean"]))
        if "centroid_std" in scalars:
            tags[f"{p}LIBROSA_SPECTRAL_CENTROID_SD"] = str(_safe_int(scalars["centroid_std"]))

        if "rms_mean" in scalars:
            tags[f"{p}LIBROSA_RMS_MEAN"] = str(_safe_int(scalars["rms_mean"], 100))
        if "rms_peak" in scalars:
            tags[f"{p}LIBROSA_RMS_PEAK"] = str(_safe_int(scalars["rms_peak"], 100))
        if "energy" in scalars:
            tags[f"{p}LIBROSA_ENERGY"] = str(_safe_int(scalars["energy"], 100))
        if "flatness" in scalars:
            tags[f"{p}LIBROSA_FLATNESS"] = str(_safe_int(scalars["flatness"], 100))

        # 配列型指標 (Contrast, MFCC)
        contrast_bands = scalars.get("contrast_bands", [])
        for i, val in enumerate(contrast_bands):
            tags[f"{p}LIBROSA_CONTRAST_B{i}"] = str(_safe_int(val, 100))

        mfccs = scalars.get("mfccs", [])
        for i, val in enumerate(mfccs):
            tags[f"{p}LIBROSA_MFCC{i:02d}"] = str(_safe_int(val, 100))

    # 2. Essentia 特徴量
    predictions = essentia_data.get("predictions", {})
    if predictions:
        model_groups: dict[str, list[tuple[str, float]]] = {}

        for k, v in predictions.items():
            prob = float(v)
            tags[f"{p}{k}"] = str(_safe_int(prob, 1000))

            raw_key = k[9:] if k.startswith("ESSENTIA_") else k
            matched_model = None
            class_name = ""

            for mkey in sorted(MODEL_KEYS, key=len, reverse=True):
                if raw_key.startswith(mkey + "_"):
                    matched_model = mkey
                    class_name = raw_key[len(mkey) + 1:]
                    break

            if matched_model and class_name:
                if matched_model not in model_groups:
                    model_groups[matched_model] = []
                model_groups[matched_model].append((class_name, prob))

        # 各モデルの Top (最高確率) クラスを文字列タグ挿入
        for mkey, items in model_groups.items():
            if items:
                top_class, top_prob = max(items, key=lambda x: x[1])
                tags[f"{p}ESSENTIA_{mkey}_TOP"] = top_class

    # 3. Tensor 特徴量 (存在する場合)
    if tensor_data:
        tensor_feats = tensor_data.get("features", tensor_data)
        if isinstance(tensor_feats, dict):
            for tk, tv in tensor_feats.items():
                if isinstance(tv, (int, float)):
                    tags[f"{p}TENSOR_{tk.upper()}"] = _safe_float_str(tv)

    return tags

def write_flac_tags_with_retry(file_path: str, tags: dict, retry_count: int = 5, retry_delay: float = 3.0):
    logger = logging.getLogger("FlacTagger")

    if not os.path.exists(file_path):
        logger.error(f"指定された FLAC ファイルが存在いたしません: {file_path}")
        sys.exit(1)

    stat_info = os.stat(file_path)
    ctime_val = getattr(stat_info, "st_ctime", None)
    atime_val = stat_info.st_atime
    mtime_val = stat_info.st_mtime

    attempt = 0
    success = False

    while attempt < retry_count:
        attempt += 1
        try:
            dir_path = os.path.dirname(os.path.abspath(file_path))
            fd, tmp_path = tempfile.mkstemp(dir=dir_path, suffix=".flac")
            os.close(fd)

            try:
                shutil.copy2(file_path, tmp_path)
                flac = FLAC(tmp_path)

                for k, v in tags.items():
                    if isinstance(v, list):
                        flac[k] = [str(item) for item in v]
                    else:
                        flac[k] = [str(v)]

                flac.save()

                try:
                    os.replace(tmp_path, file_path)
                except OSError:
                    shutil.move(tmp_path, file_path)

                # タイムスタンプ復元
                set_win_timestamps(file_path, atime_val, mtime_val, ctime_val)
                success = True
                logger.info(f"FLAC タグを成功裏に書き込みましたわ！ ({len(tags)} タグ) -> {os.path.basename(file_path)}")
                break

            finally:
                if os.path.exists(tmp_path):
                    try:
                        os.remove(tmp_path)
                    except Exception:
                        pass

        except (PermissionError, OSError) as e:
            logger.warning(f"ファイル書き込みがブロックされましたの ({attempt}/{retry_count}): {e}")
            if attempt < retry_count:
                logger.info(f"{retry_delay} 秒待機してリトライいたしますわ...")
                time.sleep(retry_delay)
            else:
                logger.error(f"ファイル書き込みの最大リトライ回数 ({retry_count}) に到達いたしました。")
                raise e

def main():
    setup_logger()
    logger = logging.getLogger("FlacTagger")

    parser = argparse.ArgumentParser(description="FLAC VorbisComment Tagger")
    parser.add_argument("--flac-path", required=True, help="Target FLAC file path")
    parser.add_argument("--json-path", required=True, help="Librosa JSON path")
    parser.add_argument("--predictions-json-path", required=False, default="", help="Essentia JSON path")
    parser.add_argument("--tensor-json-path", required=False, default="", help="Tensor JSON path")
    parser.add_argument("--prefix", required=False, default="", help="Optional prefix for CUE tracks")
    args = parser.parse_args()

    config = load_config()
    retry_count = int(config.get("python_env", {}).get("file_retry_count", 5))
    retry_delay = float(config.get("python_env", {}).get("file_retry_delay_sec", 3))

    librosa_data = {}
    if os.path.exists(args.json_path):
        try:
            with open(args.json_path, "r", encoding="utf-8") as f:
                librosa_data = json.load(f)
        except Exception as e:
            logger.warning(f"Librosa JSON のパースに失敗いたしました: {e}")

    essentia_data = {}
    if args.predictions_json_path and os.path.exists(args.predictions_json_path):
        try:
            with open(args.predictions_json_path, "r", encoding="utf-8") as f:
                essentia_data = json.load(f)
        except Exception as e:
            logger.warning(f"Essentia JSON のパースに失敗いたしました: {e}")

    tensor_data = {}
    if args.tensor_json_path and os.path.exists(args.tensor_json_path):
        try:
            with open(args.tensor_json_path, "r", encoding="utf-8") as f:
                tensor_data = json.load(f)
        except Exception as e:
            logger.warning(f"Tensor JSON のパースに失敗いたしました: {e}")

    tags = build_flac_tags(librosa_data, essentia_data, tensor_data, prefix=args.prefix)
    if not tags:
        logger.warning("書き込むべきタグ情報が生成されませんでした。")
        sys.exit(0)

    try:
        write_flac_tags_with_retry(args.flac_path, tags, retry_count=retry_count, retry_delay=retry_delay)
    except Exception as e:
        logger.exception("FLAC タグ書き込み中にエラーが発生いたしましたわ！")
        sys.exit(1)

    sys.exit(0)

if __name__ == "__main__":
    main()
