"""
flac_tagger.py
==============
解析結果 (Librosa, Essentia 453クラス確率, Tensor JSON) および DB meta タグから
FLAC VorbisComment タグ辞書を統合生成し、
圏論的 UPSERT 射 (Idempotent Morphism) として mutagen 既存タグの不足分 (missing_tags) のみを補完検出し、
緑色ログ表示、アトミック書き込みおよび Windows タイムスタンプ保護（mtime, atime, ctime）を行って
FLAC 本体へ安全に焼き込むモジュールですわ！
"""

import argparse
import contextlib
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

# ANSI カラー定数 (緑色 Log 用)
GREEN = "\033[32m"
RESET = "\033[0m"
BOLD_GREEN = "\033[1;32m"

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format=f"{GREEN}[%(levelname)s] [FlacTagger] %(message)s{RESET}",
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

# 必須個別タグとして 1000倍整数出力するモデル群
EXPLICIT_INDIVIDUAL_MODELS = {
    "APPROACHABILITY_3C", "DANCEABILITY", "ENGAGEMENT_3C",
    "GENDER", "GENRE_DORTMUND", "GENRE_ELECTRONIC",
    "GENRE_ROSAMERICA", "GENRE_TZANETAKIS", "MOOD_ACOUSTIC",
    "MOOD_AGGRESSIVE", "MOOD_ELECTRONIC", "MOOD_HAPPY",
    "MOOD_PARTY", "MOOD_RELAXED", "MOOD_SAD", "VOICE_INSTRUMENTAL"
}

MODEL_KEYS = [
    "APPROACHABILITY_3C", "DANCEABILITY", "ENGAGEMENT_3C", "FS_LOOP_DS",
    "GENDER", "GENRE_DISCOGS400", "GENRE_DORTMUND", "GENRE_ELECTRONIC",
    "GENRE_ROSAMERICA", "GENRE_TZANETAKIS", "MOODS_MIREX", "MOOD_ACOUSTIC",
    "MOOD_AGGRESSIVE", "MOOD_ELECTRONIC", "MOOD_HAPPY", "MOOD_PARTY",
    "MOOD_RELAXED", "MOOD_SAD", "VOICE_INSTRUMENTAL"
]

def parse_tags_from_meta_dict(meta: dict, prefix: str = "") -> dict[str, str]:
    tags: dict[str, str] = {}
    if not isinstance(meta, dict):
        return tags

    target_tr_num = None
    if prefix and prefix.upper().startswith("CUE_TRACK"):
        try:
            target_tr_num = int(prefix.upper().replace("CUE_TRACK", ""))
        except ValueError:
            pass

    model_groups: dict[str, list[tuple[str, float]]] = {}

    for k, v in meta.items():
        k_lower = k.lower()
        val_str = str(v[0]) if isinstance(v, list) and v else str(v)

        tr_match = re.match(r"^(?:cue_?)?track_?(\d+)_(.+)$", k_lower)
        if tr_match:
            tr_num = int(tr_match.group(1))
            if target_tr_num is not None and tr_num != target_tr_num:
                continue
            raw_tag = tr_match.group(2)
        else:
            if target_tr_num is not None:
                continue
            raw_tag = k_lower

        if raw_tag.startswith("essentia_"):
            clean_k = raw_tag.upper()
            tag_key = f"{prefix + '_' if prefix else ''}{clean_k}"

            if clean_k.endswith("_TOP") or clean_k.endswith("_TOP5"):
                tags[tag_key] = val_str
                continue

            raw_name = clean_k[9:]
            matched_model = None
            class_name = ""
            for mkey in sorted(MODEL_KEYS, key=len, reverse=True):
                if raw_name.startswith(mkey + "_"):
                    matched_model = mkey
                    class_name = raw_name[len(mkey) + 1:]
                    break

            try:
                fval = float(val_str)
                prob = fval if (0.0 <= fval <= 1.0 and "." in val_str) else fval / 1000.0
                int_val_str = str(_safe_int(prob, 1000))
            except ValueError:
                prob = 0.0
                int_val_str = val_str

            if matched_model in EXPLICIT_INDIVIDUAL_MODELS:
                tags[tag_key] = int_val_str

            if matched_model and class_name:
                if matched_model not in model_groups:
                    model_groups[matched_model] = []
                model_groups[matched_model].append((class_name, prob))

        elif raw_tag.startswith("librosa_") or raw_tag.startswith("tensor_"):
            clean_k = raw_tag.upper()
            tag_key = f"{prefix + '_' if prefix else ''}{clean_k}"
            tags[tag_key] = val_str

    for mkey, items in model_groups.items():
        sorted_items = sorted(items, key=lambda x: x[1], reverse=True)
        if sorted_items:
            top_tag = f"{prefix + '_' if prefix else ''}ESSENTIA_{mkey}_TOP"
            if top_tag not in tags:
                tags[top_tag] = sorted_items[0][0]

            top5_tag = f"{prefix + '_' if prefix else ''}ESSENTIA_{mkey}_TOP5"
            top5_classes = [it[0] for it in sorted_items[:5]]
            tags[top5_tag] = "; ".join(top5_classes)

    return tags

def build_flac_tags(librosa_data: dict, essentia_data: dict, tensor_data: dict, prefix: str = "") -> dict[str, str]:
    p = f"{prefix}_" if prefix else ""
    tags: dict[str, str] = {}

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
        
        if "zcr_mean" in scalars:
            tags[f"{p}LIBROSA_ZCR"] = _safe_float_str(scalars["zcr_mean"])
        elif "zcr" in scalars:
            tags[f"{p}LIBROSA_ZCR"] = _safe_float_str(scalars["zcr"])
            
        if "snr" in scalars and scalars["snr"] is not None:
            tags[f"{p}LIBROSA_SNR"] = _safe_float_str(scalars["snr"])
        if "rolloff_mean" in scalars:
            tags[f"{p}LIBROSA_ROLLOFF"] = _safe_float_str(scalars["rolloff_mean"])
        
        # NAP & HNR (dB) タグ生成
        if "nap" in scalars:
            tags[f"{p}LIBROSA_NAP"] = _safe_float_str(scalars["nap"])
        if "hnr_db" in scalars:
            tags[f"{p}LIBROSA_HNR_DB"] = _safe_float_str(scalars["hnr_db"])
            
        if "hnr" in scalars:
            tags[f"{p}LIBROSA_HNR"] = _safe_float_str(scalars["hnr"])
        elif "hnr_db" in scalars:
            tags[f"{p}LIBROSA_HNR"] = _safe_float_str(scalars["hnr_db"])

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

        contrast_bands = scalars.get("contrast_bands", [])
        for i, val in enumerate(contrast_bands):
            tags[f"{p}LIBROSA_CONTRAST_B{i}"] = str(_safe_int(val, 100))

        mfccs = scalars.get("mfccs", [])
        for i, val in enumerate(mfccs):
            tags[f"{p}LIBROSA_MFCC{i:02d}"] = str(_safe_int(val, 100))

    predictions = essentia_data.get("predictions", {})
    if predictions:
        model_groups: dict[str, list[tuple[str, float]]] = {}

        for k, v in predictions.items():
            prob = float(v)
            raw_key = k[9:] if k.startswith("ESSENTIA_") else k
            matched_model = None
            class_name = ""

            for mkey in sorted(MODEL_KEYS, key=len, reverse=True):
                if raw_key.startswith(mkey + "_"):
                    matched_model = mkey
                    class_name = raw_key[len(mkey) + 1:]
                    break

            if matched_model in EXPLICIT_INDIVIDUAL_MODELS:
                tags[f"{p}{k}"] = str(_safe_int(prob, 1000))

            if matched_model and class_name:
                if matched_model not in model_groups:
                    model_groups[matched_model] = []
                model_groups[matched_model].append((class_name, prob))

        for mkey, items in model_groups.items():
            sorted_items = sorted(items, key=lambda x: x[1], reverse=True)
            if sorted_items:
                tags[f"{p}ESSENTIA_{mkey}_TOP"] = sorted_items[0][0]
                top5_classes = [it[0] for it in sorted_items[:5]]
                tags[f"{p}ESSENTIA_{mkey}_TOP5"] = "; ".join(top5_classes)

    if tensor_data:
        tensor_feats = tensor_data.get("features", tensor_data)
        if isinstance(tensor_feats, dict):
            for tk, tv in tensor_feats.items():
                if isinstance(tv, (int, float)):
                    tags[f"{p}TENSOR_{tk.upper()}"] = _safe_float_str(tv)

    return tags

@contextlib.contextmanager
def flac_file_lock(file_path: str, timeout: float = 60.0, retry_delay: float = 0.5):
    """
    FLAC ファイルに対するプロセス間排他ロック (Inter-Process Exclusive Lock) を提供しますわ。
    同一FLACファイル（CUE複数トラック等）への並行書き込み競合（Lost Update & Access Denied）を完全防止いたします。
    """
    logger = logging.getLogger("FlacTagger")
    lock_path = f"{file_path}.tagger.lock"
    start_time = time.time()
    lock_fd = None

    while True:
        try:
            if sys.platform == "win32":
                import msvcrt
                # 共有書き込みを許容しつつロックバイトを獲得
                lock_fd = os.open(lock_path, os.O_RDWR | os.O_CREAT)
                msvcrt.locking(lock_fd, msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                lock_fd = os.open(lock_path, os.O_RDWR | os.O_CREAT)
                fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except (OSError, PermissionError, BlockingIOError):
            if lock_fd is not None:
                try:
                    os.close(lock_fd)
                except Exception:
                    pass
                lock_fd = None
            if time.time() - start_time > timeout:
                raise TimeoutError(f"FLAC ファイルロック取得がタイムアウトいたしましたわ ({timeout}秒): {file_path}")
            # ジッター（0.1〜0.3秒）を加えて同期的な衝突を分散
            jitter = (time.time_ns() % 200) / 1000.0
            time.sleep(retry_delay + jitter)

    try:
        yield
    finally:
        if lock_fd is not None:
            try:
                if sys.platform == "win32":
                    import msvcrt
                    try:
                        msvcrt.locking(lock_fd, msvcrt.LK_UNLCK, 1)
                    except Exception:
                        pass
                else:
                    import fcntl
                    try:
                        fcntl.flock(lock_fd, fcntl.LOCK_UN)
                    except Exception:
                        pass
                os.close(lock_fd)
            except Exception:
                pass
            try:
                if os.path.exists(lock_path):
                    os.remove(lock_path)
            except Exception:
                pass

def write_flac_tags_with_retry(
    file_path: str,
    tags: dict,
    retry_count: int = 5,
    retry_delay: float = 3.0,
    lock_timeout: float = 60.0,
    force: bool = False
):
    """
    排他ロック下で FLAC タグの補完書き込み (UPSERT Morphism) を実行いたしますわ。
    一時ファイルは .tmp 拡張子で生成し、外部プロセス（AV / Indexer / Player）の干渉を回避します。
    """
    logger = logging.getLogger("FlacTagger")

    if not os.path.exists(file_path):
        logger.error(f"指定された FLAC ファイルが存在いたしません: {file_path}")
        sys.exit(1)

    stat_info = os.stat(file_path)
    ctime_val = getattr(stat_info, "st_ctime", None)
    atime_val = stat_info.st_atime
    mtime_val = stat_info.st_mtime

    with flac_file_lock(file_path, timeout=lock_timeout):
        # ロック獲得後に最新の VorbisComment を再確認（先行ワーカーによる書き込みを反映）
        tags_to_write = tags
        if not force:
            try:
                flac_cur = FLAC(file_path)
                existing_tags = {k.upper(): v for k, v in flac_cur.tags.items()} if flac_cur.tags else {}
                missing = {}
                for k, v in tags.items():
                    k_upper = k.upper()
                    if k_upper not in existing_tags or not existing_tags[k_upper]:
                        missing[k] = v
                if not missing:
                    logger.info(f"{GREEN}[UPSERT Morphism]{RESET} すべてのタグが既に書き込み済みです: {os.path.basename(file_path)}")
                    return
                tags_to_write = missing
            except Exception as e:
                logger.warning(f"既存 FLAC タグの読込中に警告が発生いたしました (全上書きへフォールバック): {e}")
                tags_to_write = tags

        attempt = 0
        dir_path = os.path.dirname(os.path.abspath(file_path))

        # Storage Defense: 書き込み前に同一ドライブの空き容量を事前検証 (config.toml tagger_disk_margin_ratio)
        config = load_config()
        margin_ratio = float(config.get("orchestrator", {}).get("tagger_disk_margin_ratio", 1.5))
        if margin_ratio <= 0:
            margin_ratio = 1.5
        required_disk_bytes = int(stat_info.st_size * margin_ratio)

        try:
            disk_usage = shutil.disk_usage(dir_path)
            if disk_usage.free < required_disk_bytes:
                err_msg = (
                    f"ディスク空き容量不足のため FLAC タグ書き込みを安全に中断いたしましたわ！ "
                    f"(必要: {required_disk_bytes / (1024*1024):.2f} MB, 空き: {disk_usage.free / (1024*1024):.2f} MB) -> {os.path.basename(file_path)}"
                )
                logger.error(f"[Storage Defense] {err_msg}")
                raise OSError(err_msg)
        except OSError:
            raise
        except Exception as disk_err:
            logger.warning(f"ディスク容量確認中に警告が発生いたしました (書き込みを継続試行いたします): {disk_err}")

        while attempt < retry_count:
            attempt += 1
            # .flac ではなく .tmp 拡張子を使用し、メディア監視・AVスキャンの誤検知を遮断
            tmp_path = os.path.join(dir_path, f".~tagger_{os.getpid()}_{time.time_ns()}.tmp")
            try:
                shutil.copy2(file_path, tmp_path)
                flac = FLAC(tmp_path)

                for k, v in tags_to_write.items():
                    if isinstance(v, list):
                        flac[k] = [str(item) for item in v]
                    else:
                        flac[k] = [str(v)]

                flac.save()

                try:
                    os.replace(tmp_path, file_path)
                except OSError:
                    shutil.move(tmp_path, file_path)

                set_win_timestamps(file_path, atime_val, mtime_val, ctime_val)
                logger.info(f"{BOLD_GREEN}[UPSERT Morphism]{GREEN} FLAC タグを成功裏に補完書き込みいたしましたわ！ ({len(tags_to_write)} タグ) -> {os.path.basename(file_path)}{RESET}")
                break

            except Exception as e:
                logger.warning(f"ファイル書き込みがブロック/失敗いたしましたの ({attempt}/{retry_count}): {e}")
                if attempt < retry_count:
                    jitter = (time.time_ns() % 500) / 1000.0
                    sleep_time = retry_delay + jitter
                    logger.info(f"{sleep_time:.2f} 秒待機してリトライいたしますわ...")
                    time.sleep(sleep_time)
                else:
                    logger.error(f"ファイル書き込みの最大リトライ回数 ({retry_count}) に到達いたしました。")
                    raise e
            finally:
                if os.path.exists(tmp_path):
                    try:
                        os.remove(tmp_path)
                    except Exception:
                        pass

def main():
    setup_logger()
    logger = logging.getLogger("FlacTagger")

    parser = argparse.ArgumentParser(description="FLAC VorbisComment Tagger (UPSERT Morphism)")
    parser.add_argument("--flac-path", required=True, help="Target FLAC file path")
    parser.add_argument("--json-path", required=True, help="Librosa JSON path")
    parser.add_argument("--predictions-json-path", required=False, default="", help="Essentia JSON path")
    parser.add_argument("--tensor-json-path", required=False, default="", help="Tensor JSON path")
    parser.add_argument("--prefix", required=False, default="", help="Optional prefix for CUE tracks")
    parser.add_argument("--force", action="store_true", help="Force overwrite all tags even if present")
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

    t_tag_start = time.perf_counter()
    expected_tags = build_flac_tags(librosa_data, essentia_data, tensor_data, prefix=args.prefix)
    if not expected_tags:
        logger.warning("書き込むべきタグ情報が生成されませんでした。")
        print(json.dumps({"status": "skipped", "profile": {"write": 0.0}}))
        sys.exit(0)

    try:
        write_flac_tags_with_retry(
            args.flac_path,
            expected_tags,
            retry_count=retry_count,
            retry_delay=retry_delay,
            force=args.force
        )
    except Exception as e:
        logger.exception("FLAC タグ書き込み中にエラーが発生いたしましたわ！")
        sys.exit(1)

    tag_sec = time.perf_counter() - t_tag_start
    print(json.dumps({"status": "success", "profile": {"write": tag_sec}}))
    sys.exit(0)

if __name__ == "__main__":
    main()

