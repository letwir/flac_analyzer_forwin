"""
Pipeline module for FLAC Analyzer
================================
音声デコード、Cuesheetパース、セグメント切り出し、
および解析フローを合成する自然変換（Natural Transformation）の合成層ですわ。
"""

import logging
import os
import re
import shutil
import tempfile
import gc
import time
from multiprocessing import sharedctypes
from typing import Any

import numpy as np
import soundfile as sf
from mutagen.flac import FLAC
import load_wave
import flac_decode

import models
from analyzer import (
    DemucsFeatures,
    EssentiaFeatures,
    librosa_extractor,
    stem_extractor,
)
from analyzer_worker import process_stem, process_stem_shm
from concurrent.futures import ProcessPoolExecutor, as_completed
import json
import math

class SafeAudioJSONEncoder(json.JSONEncoder):
    def default(self, o):
        try:
            import numpy as np
            if isinstance(o, (np.floating, np.integer)):
                return o.item()
            if isinstance(o, np.ndarray):
                return o.tolist()
        except ImportError:
            pass
        return super().default(o)

    def iterencode(self, o, _one_shot=False):
        try:
            import numpy as np
            has_numpy = True
        except ImportError:
            has_numpy = False

        def sanitize(obj):
            if isinstance(obj, dict):
                return {k: sanitize(v) for k, v in obj.items()}
            elif isinstance(obj, (list, tuple)):
                return [sanitize(x) for x in obj]
            elif has_numpy and isinstance(obj, (float, np.floating)):
                if np.isinf(obj) or np.isnan(obj):
                    return None
                return float(obj)
            elif not has_numpy and isinstance(obj, float):
                if math.isinf(obj) or math.isnan(obj):
                    return None
                return obj
            elif has_numpy and isinstance(obj, (int, np.integer)):
                return int(obj)
            elif has_numpy and isinstance(obj, np.ndarray):
                return [sanitize(x) for x in obj.tolist()]
            return obj

        return super().iterencode(sanitize(o), _one_shot=_one_shot)


MODELS_DIR = "./models"

# ─────────────────────────────────────────────
# グローバル プロセスプール (Windows向け最適化)
# ─────────────────────────────────────────────
GLOBAL_PROCESS_POOL: ProcessPoolExecutor | None = None

def get_process_pool() -> ProcessPoolExecutor:
    """アプリ起動時に一度だけプロセスプールを生成し、以降は使い回しますの。"""
    global GLOBAL_PROCESS_POOL
    if GLOBAL_PROCESS_POOL is None:
        workers = get_segment_workers()
        GLOBAL_PROCESS_POOL = ProcessPoolExecutor(max_workers=workers)
        logging.info(f"[ProcessPool] グローバルプロセスプールを作成いたしましたわ！ (workers: {workers})")
    return GLOBAL_PROCESS_POOL


def get_segment_workers() -> int:
    """遅延資源評価 (Lazy evaluation) により、インポート時の副作用を排除しますの。"""
    import psutil

    ram_gb = psutil.virtual_memory().total / (1024**3)
    cpu_threads = os.cpu_count() or 4
    if ram_gb <= 8:
        return 1
    elif ram_gb <= 16:
        return 1
    elif ram_gb <= 32:
        return 2
    else:
        return min(8, cpu_threads // 4)
def analyze_segment_pipeline(
    audio_segment: np.ndarray,
    sr: int,
    essentia_models: dict,
    models_dir: str,
) -> tuple[dict[str, Any], EssentiaFeatures | None, DemucsFeatures, str]:
    """
    1セグメント分の特徴量を抽出するパイプラインですわ！
    - GLOBAL_DEMUCS による波形分離 (StemContext の取得)
    - 各ソースへの Librosa 特徴量並列分配抽出 (Applicative & Product)
    - mix への Essentia 直列推論
    """
    import time
    import multiprocessing
    proc_name = multiprocessing.current_process().name

    # 1. 前段: 波形分離 (GLOBAL_DEMUCS による StemContext 取得)
    t_start_demucs = time.perf_counter()
    stem_context = models.GLOBAL_DEMUCS.separate(audio_segment, sr)
    logging.info(
        f"[{proc_name}] [Morphism] [Demucs] [Separation] "
        f"波形分離処理(GLOBAL_DEMUCS)を完了いたしましたわ (経過: {time.perf_counter() - t_start_demucs:.4f}s)"
    )

    # mix ステムから CoMonad 方式で audio_hash を回収しますわ！
    mix_hash = stem_context.stems["mix"].audio_hash

    # 1.5 & 2. 中段: Librosa解析を ProcessPoolExecutor によるプロセス並列で実行しますわ！
    # 共有メモリを使用して巨大な配列コピー（pickle）を削減しますの。
    import uuid
    import shm_interop
    
    track_features: dict[str, Any] = {}
    shm_list: list[Any] = []
    
    t_start_librosa = time.perf_counter()
    pool = get_process_pool()
    
    logging.info(
        f"[{proc_name}] [Morphism] [Librosa] [Extraction] "
        f"共有メモリ確保 ＆ ProcessPoolExecutor へ各ステムの解析タスクをディスパッチしますわ..."
    )
    
    try:
        futures = {}
        for name, ctx in stem_context.stems.items():
            # 共有メモリ名を作成 (Windows Native SHM)
            shm_name = f"Local\\FlacShm_{uuid.uuid4().hex}"
            
            # Python側で共有メモリを確保し、Zero-copyで波形データを書き込みますの
            shm = shm_interop.write_to_shm(shm_name, ctx.y)
            shm_list.append(shm)
            
            # 子プロセスには、共有メモリ名、形状、データ型の文字列、レートだけを渡しますわ！
            # これにより 400MB の IPC コピーを完全に葬り去ることができますの
            fut = pool.submit(
                process_stem_shm,
                name,
                shm_name,
                ctx.y.shape,
                str(ctx.y.dtype),
                ctx.sr
            )
            futures[fut] = name
            
        for fut in as_completed(futures):
            res_name = futures[fut]
            try:
                track_features[res_name] = fut.result()
            except Exception as e:
                logging.error(f"ソース [{res_name}] のLibrosa解析エラー: {e}", exc_info=True)
                
    except Exception as e_pool:
        logging.warning(
            f"[{proc_name}] [Morphism] [Librosa] [Extraction] "
            f"プロセス並列実行中に例外発生いたしましたわ、直列フォールバックしますの: {e_pool}"
        )
        # 直列フォールバック時は、共有メモリを使わずそのまま実行しますわ
        for name, ctx in stem_context.stems.items():
            try:
                track_features[name] = process_stem(name, ctx.y, ctx.sr)
            except Exception as e:
                logging.error(f"ソース [{name}] のLibrosa解析エラー(直列): {e}", exc_info=True)
    finally:
        # 親プロセス側で確保したすべての共有メモリを確実に close して解放しますわ！
        # (これを怠ると Windows でメモリリークしてしまいますの)
        for shm in shm_list:
            try:
                shm.close()
            except Exception as e_del:
                logging.warning(f"共有メモリ解放エラー: {e_del}")
                
    logging.info(
        f"[{proc_name}] [Morphism] [Librosa] [Extraction] "
        f"全ステムの Librosa 特徴量抽出を完了いたしましたわ (経過: {time.perf_counter() - t_start_librosa:.4f}s)"
    )

    # 2.5 同期後処理 (Post-Bind Overwrite)
    # ── Step 1: ステム別エネルギー比率を算出し SNR を上書き ──
    t_start_snr = time.perf_counter()
    total_stem_energy = sum(
        feat.energy for name, feat in track_features.items() if name != "mix"
    )
    epsilon = 1e-10
    for name, feat in track_features.items():
        if name == "mix":
            continue
        feat.snr = (feat.energy + epsilon) / (total_stem_energy + 2 * epsilon)

    # ── Step 2: DemucsFeatures 構築 ──
    # 各ステムの LibrosaFeatures から値を回収して DemucsFeatures を組み立てますの。
    stems_feats = {
        name: feat for name, feat in track_features.items() if name != "mix"
    }
    energy_ratios = {
        name: float((feat.energy + epsilon) / (total_stem_energy + 2 * epsilon))
        for name, feat in stems_feats.items()
    }
    demucs_feats = DemucsFeatures(
        stems=stems_feats,
        energy_ratios=energy_ratios
    )
    logging.info(
        f"[{proc_name}] [Endomorphism] [Post-processing] [SNR-Overwrite] "
        f"ステム相対エネルギー比率(SNR)の上書きを完了いたしましたわ (経過: {time.perf_counter() - t_start_snr:.4f}s)"
    )

    # 3. 中段: Essentia推論 (mix のみに対して同期実行)
    essentia_feats = None
    if models.GLOBAL_ONNX_SESSIONS.get("effnet") is not None:
        t_start_essentia = time.perf_counter()
        logging.info(
            f"[{proc_name}] [Morphism] [Essentia] [ONNX-Inference] "
            f"Essentia (EffNet) 分類器{len(essentia_models)}基による推論を開始しますわ"
        )
        try:
            patches = models.extract_mel_patches(audio_segment, sr, n_patches=64)
            preds_dict = models.run_essentia_serialized(patches, essentia_models)
            essentia_feats = EssentiaFeatures(preds_dict)
            logging.info(
                f"[{proc_name}] [Morphism] [Essentia] [ONNX-Inference] "
                f"Essentia 分類推論を完了いたしましたわ (経過: {time.perf_counter() - t_start_essentia:.4f}s)"
            )
        except Exception as e:
            logging.error(f"Essentia解析エラー: {e}", exc_info=True)

    return track_features, essentia_feats, demucs_feats, mix_hash




def get_cue_tag_fallback(
    meta: FLAC, track_num: int, tag_name: str, file_global_fallback: str | None = None
) -> str | None:
    """
    指定したトラック番号とタグ名について、FLACのタグから大文字小文字を無視してマッチする値を取得しますわ！
    """
    target_keys = [
        f"cue_track{track_num:02d}_{tag_name}",
        f"cue_track_{track_num:02d}_{tag_name}",
        f"cue_track{track_num}_{tag_name}",
        f"cue_track_{track_num}_{tag_name}",
        f"track{track_num:02d}_{tag_name}",
        f"track{track_num}_{tag_name}",
        f"cue_track{track_num:02d}_{tag_name}sort",
        f"cue_track{track_num:02d}_{tag_name}_sort",
    ]
    if tag_name == "artist":
        target_keys.extend(
            [
                f"cue_track{track_num:02d}_performer",
                f"cue_track_{track_num:02d}_performer",
                f"cue_track{track_num}_performer",
                f"cue_track_{track_num}_performer",
            ]
        )

    meta_lower = {k.lower(): v for k, v in meta.items()}

    for tk in target_keys:
        val = meta_lower.get(tk)
        if val and val[0]:
            return val[0]

    if file_global_fallback:
        val = meta_lower.get(file_global_fallback.lower())
        if val and val[0]:
            return val[0]

    return None


def parse_cue_segments(cue_text: str, total_samples: int, sr: int) -> list[dict[str, Any]]:
    tracks: list[dict[str, Any]] = []
    
    # 正規表現パターンの定義 (大文字小文字・前後の余白を無視)
    track_pat = re.compile(r'^\s*TRACK\s+(\d+)\s+AUDIO', re.IGNORECASE)
    title_pat = re.compile(r'^\s*TITLE\s+(?:"([^"]*)"|(.*))$', re.IGNORECASE)
    performer_pat = re.compile(r'^\s*PERFORMER\s+(?:"([^"]*)"|(.*))$', re.IGNORECASE)
    index_pat = re.compile(r'^\s*INDEX\s+01\s+(\d+):(\d+):(\d+)', re.IGNORECASE)

    current_track = None
    current_title = ""
    current_artist = ""
    raw_tracks = []

    for line in cue_text.splitlines():
        line = line.strip()
        if not line:
            continue
        
        m_tr = track_pat.match(line)
        if m_tr:
            current_track = int(m_tr.group(1))
            current_title = ""
            current_artist = ""
            continue
        
        m_ti = title_pat.match(line)
        if m_ti and current_track is not None:
            current_title = (m_ti.group(1) or m_ti.group(2) or "").strip()
            continue
            
        m_pe = performer_pat.match(line)
        if m_pe and current_track is not None:
            current_artist = (m_pe.group(1) or m_pe.group(2) or "").strip()
            continue
            
        m_idx = index_pat.match(line)
        if m_idx and current_track is not None:
            m = int(m_idx.group(1))
            s = int(m_idx.group(2))
            f = int(m_idx.group(3))
            start_sample = int((m * 60 + s + f / 75.0) * sr)
            raw_tracks.append(
                {
                    "track": current_track,
                    "start": start_sample,
                    "title": current_title,
                    "artist": current_artist,
                }
            )

    segments: list[dict[str, Any]] = []
    for i, t in enumerate(raw_tracks):
        start = int(t["start"])
        end_val = int(raw_tracks[i + 1]["start"]) if i + 1 < len(raw_tracks) else total_samples
        if start < total_samples:
            segments.append(
                {
                    "track": int(t["track"]),
                    "start": start,
                    "end": min(end_val, total_samples),
                    "title": t["title"],
                    "artist": t["artist"],
                }
            )
    return segments


def pack_essentia_multi_tags(tags: dict) -> dict:
    """
    FLACに書き込むタグ辞書を受け取り、個別に出力されている ESSENTIA_{MODEL}_{CLASS} タグを
    複数値（list[str]）の単一タグ ESSENTIA_{MODEL} = ["CLASS:確率"] に集約しますの。
    """
    MODEL_KEYS = [
        "APPROACHABILITY_3C", "DANCEABILITY", "ENGAGEMENT_3C", "FS_LOOP_DS",
        "GENDER", "GENRE_DORTMUND", "GENRE_ELECTRONIC", "GENRE_ROSAMERICA",
        "GENRE_TZANETAKIS", "MOODS_MIREX", "MOOD_ACOUSTIC", "MOOD_AGGRESSIVE",
        "MOOD_ELECTRONIC", "MOOD_HAPPY", "MOOD_PARTY", "MOOD_RELAXED",
        "MOOD_SAD", "VOICE_INSTRUMENTAL", "GENRE_DISCOGS400"
    ]

    packed: dict[str, Any] = {}
    essentia_groups: dict[str, list[tuple[str, float]]] = {}

    for k, v in tags.items():
        prefix = ""
        raw_key = k
        if k.startswith("CUE_TRACK"):
            parts = k.split("_", 2)
            if len(parts) >= 3 and parts[2].startswith("ESSENTIA_"):
                prefix = f"{parts[0]}_{parts[1]}_"
                raw_key = parts[2]

        if not raw_key.startswith("ESSENTIA_"):
            packed[k] = v
            continue

        raw = raw_key[9:]  # "ESSENTIA_" を除いた部分
        matched_model = None
        for mkey in sorted(MODEL_KEYS, key=len, reverse=True):
            if raw.startswith(mkey + "_"):
                matched_model = mkey
                class_name = raw[len(mkey) + 1:]
                break

        if not matched_model:
            parts = raw.rsplit("_", 1)
            if len(parts) == 2:
                matched_model, class_name = parts
            else:
                matched_model, class_name = "UNKNOWN", raw

        try:
            prob = float(v) / 1000.0
        except ValueError:
            prob = 0.0

        group_key = f"{prefix}ESSENTIA_{matched_model}"
        if group_key not in essentia_groups:
            essentia_groups[group_key] = []
        essentia_groups[group_key].append((class_name, prob))

    for gkey, items in essentia_groups.items():
        selected = [item for item in items if item[1] >= 0.1]
        if not selected:
            selected = [max(items, key=lambda x: x[1])]
        
        selected.sort(key=lambda x: x[1], reverse=True)
        values = [f"{class_name}:{int(prob * 1000)}" for class_name, prob in selected]
        packed[gkey] = values

    return packed


def write_flac_tags_atomic(file_path: str, tags: dict):
    stat_info = os.stat(file_path)
    ctime_val = stat_info.st_ctime
    atime_val = stat_info.st_atime
    mtime_val = stat_info.st_mtime

    dir_path = os.path.dirname(os.path.abspath(file_path))
    fd, tmp_path = tempfile.mkstemp(
        dir=dir_path,
        suffix=".flac",
    )
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

        try:
            if os.name == "nt":
                import ctypes
                from ctypes import wintypes

                kernel32 = ctypes.windll.kernel32  # type: ignore[attr-defined]

                GENERIC_WRITE = 0x40000000
                OPEN_EXISTING = 3
                FILE_SHARE_READ = 0x00000001
                FILE_SHARE_WRITE = 0x00000002
                FILE_SHARE_DELETE = 0x00000004

                handle = kernel32.CreateFileW(
                    file_path,
                    GENERIC_WRITE,
                    FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                    None,
                    OPEN_EXISTING,
                    0,
                    None,
                )

                if handle != -1:

                    def to_filetime(epoch_time):
                        val = int((epoch_time + 11644473600) * 10000000)
                        return wintypes.FILETIME(val & 0xFFFFFFFF, val >> 32)

                    ft_create = to_filetime(ctime_val)
                    ft_access = to_filetime(atime_val)
                    ft_write = to_filetime(mtime_val)

                    kernel32.SetFileTime(
                        handle,
                        ctypes.byref(ft_create),
                        ctypes.byref(ft_access),
                        ctypes.byref(ft_write),
                    )
                    kernel32.CloseHandle(handle)
                    logging.info(
                        f"[Timestamp Inheritance] Windows ctypesを用いてタイムスタンプを完全継承しましたわ！ (target: {os.path.basename(file_path)})"
                    )
                else:
                    logging.warning(
                        "[Timestamp Inheritance] ファイルハンドル取得に失敗いたしましたわ"
                    )
            else:
                os.utime(file_path, (atime_val, mtime_val))
                logging.info(
                    f"[Timestamp Inheritance] os.utimeを用いてアクセス・更新日時を復元しましたわ！ (target: {os.path.basename(file_path)})"
                )
        except Exception as e_time:
            logging.warning(
                f"タイムスタンプ復元中に不具合発生（無視して続行しますわ）: {e_time}"
            )

    except Exception:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)
        raise


def process_single_flac_file(file_path: str, essentia_models: dict) -> str:
    basename = os.path.basename(file_path)

    try:
        logging.info(f"解析なう: {basename}")

        preload_audio, sr = sf.read(
            file_path,
            dtype="float32",
            always_2d=False,
        )

        audio = np.ascontiguousarray(
            preload_audio,
            dtype=np.float32,
        )
        del preload_audio

        meta = FLAC(file_path)
        
        # mutagenの全メタデータを辞書化（キーは小文字、値はリスト/文字列の平坦化適用）
        raw_tags: dict[str, Any] = {}
        for k, v in meta.items():
            val_list = [str(x) for x in v]
            key_lower = k.lower()
            if len(val_list) == 1:
                raw_tags[key_lower] = val_list[0]
            elif len(val_list) == 0:
                raw_tags[key_lower] = ""
            else:
                raw_tags[key_lower] = val_list

        TRACK_TAG_PAT = re.compile(r"^(?:cue_)?track_?(\d+)_(.+)$", re.IGNORECASE)

        final_tags: dict[str, str] = {}

        cue_text = None
        segments = []

        # A) Vorbis comment から cuesheet テキストを探す
        for k in meta.keys():
            if k.lower() == "cuesheet":
                cue_text = meta[k][0]
                break

        # B) cuesheet テキストがあればパース
        if cue_text:
            segments = parse_cue_segments(cue_text, len(audio), sr)

        # C) cuesheet テキストが無いが、FLACメタデータブロックの CueSheet がある場合
        if not segments:
            cue_block = None
            for block in meta.metadata_blocks:
                if "cuesheet" in type(block).__name__.lower():
                    cue_block = block
                    break
            if cue_block:
                logging.info(
                    "\t\t[Cuesheet Block] メタデータブロックからCuesheet境界を検出いたしましたわ！"
                )
                total_samples = len(audio)
                raw_tracks = []
                for t in cue_block.tracks:
                    if t.type == 0:  # audio track
                        raw_tracks.append(
                            {
                                "track": t.track_number,
                                "start": t.start_offset,
                                "title": "",
                                "artist": "",
                            }
                        )
                raw_tracks.sort(key=lambda x: x["track"])
                for i, t in enumerate(raw_tracks):
                    start = t["start"]
                    end = (
                        raw_tracks[i + 1]["start"]
                        if i + 1 < len(raw_tracks)
                        else total_samples
                    )
                    segments.append(
                        {
                            "track": t["track"],
                            "start": start,
                   "end": int(min(end, total_samples)),
                            "title": "",
                            "artist": "",
                        }
                    )

        # D) Cuesheetもメタデータブロックも無いが、cue_trackXX_ の個別タグがある場合（フォールバック）
        if not segments:
            track_numbers = set()
            for k in meta.keys():
                m = re.match(r"(?:cue_)?track_?(\d+)_", k, re.IGNORECASE)
                if m:
                    track_numbers.add(int(m.group(1)))

            if track_numbers:
                logging.info(
                    f"\t\t[Cuesheet Tag fallback] 個別タグから {len(track_numbers)} トラックを検出いたしましたわ！"
                )
                total_samples = len(audio)
                sorted_nums = sorted(list(track_numbers))
                for num in sorted_nums:
                    segments.append(
                        {
                            "track": num,
                            "start": 0,
                            "end": total_samples,
                            "title": "",
                            "artist": "",
                        }
                    )

        # 解析・インサート実行
        if segments:
            logging.info(f"\t\t{len(segments)}曲を解析しますの！")

            resolved_tracks = []
            for seg in segments:
                num = seg["track"]

                # タイトルのマージ・フォールバック
                track_title = seg.get("title")
                if not track_title:
                    track_title = get_cue_tag_fallback(meta, num, "title")
                if not track_title:
                    track_title = f"Track {num}"

                # アーティストのマージ・フォールバック
                track_artist = seg.get("artist")
                if not track_artist:
                    track_artist = get_cue_tag_fallback(
                        meta, num, "artist", file_global_fallback="artist"
                    )
                if not track_artist:
                    track_artist = get_cue_tag_fallback(
                        meta, num, "artist", file_global_fallback="albumartist"
                    )
                if not track_artist:
                    track_artist = "Unknown"

                # コンポーザーのマージ・フォールバック
                track_composer = get_cue_tag_fallback(
                    meta, num, "composer", file_global_fallback="composer"
                )

                resolved_track = {
                    "track": num,
                    "title": track_title,
                    "artist": track_artist,
                    "start_sample": seg["start"],
                    "end_sample": seg["end"],
                    "duration": float(seg["end"] - seg["start"]) / sr,
                }
                if track_composer:
                    resolved_track["composer"] = track_composer

                resolved_tracks.append(resolved_track)

            cuesheet_payload = {"raw": cue_text, "tracks": resolved_tracks}

            for resolved in resolved_tracks:
                num = resolved["track"]
                seg_audio = audio[resolved["start_sample"] : resolved["end_sample"]]

                if len(seg_audio) < 100:
                    logging.warning(
                        f"Track {num} のサンプル数が極端に少ないため解析をスキップしますわ。"
                    )
                    continue

                track_features, essentia_feats, demucs_feats, mix_hash = (
                    analyze_segment_pipeline(seg_audio, sr, essentia_models, MODELS_DIR)
                )

                # 1. FLACタグ用 (mixの特徴量をそのトラックの代表タグとする)
                mix_lib_feat = track_features.get("mix")
                if mix_lib_feat:
                    final_tags.update(
                        mix_lib_feat.to_flac_tags(prefix=f"CUE_TRACK{num:02d}")
                    )
                if essentia_feats:
                    ess_tags = essentia_feats.to_flac_tags()
                    for k, v in ess_tags.items():
                        final_tags[f"CUE_TRACK{num:02d}_{k}"] = v
                # NEW: DEMUCS_* タグを追記 (prefix なし: トラック共通)
                demucs_tags = demucs_feats.to_flac_tags()
                for k, v in demucs_tags.items():
                    final_tags[f"CUE_TRACK{num:02d}_{k}"] = v

                # 2. Postgres INSERT用のメタデータ
                def get_tag_fallback_global(*keys, default=""):
                    for k in keys:
                        val = meta.get(k.lower())
                        if val and val[0]:
                            return val[0]
                    return default

                # 共通タグおよび自トラック用タグの抽出マージ
                track_meta = {}
                for k, v in raw_tags.items():
                    m = TRACK_TAG_PAT.match(k)
                    if m:
                        tag_track_num = int(m.group(1))
                        tag_name = m.group(2)
                        # 自トラックの個別タグであれば、プレフィックスを除いたキーでマージしますの
                        if tag_track_num == num:
                            track_meta[tag_name] = v
                        # 他トラックの個別タグは除外（スキップ）しますわ
                    else:
                        # トラック個別タグでないものは、ファイル共通タグとしてマージしますわ
                        track_meta[k] = v

                # 既存のトラック個別解決データで上書きマージ
                track_meta.update({
                    "title": resolved["title"],
                    "artist": resolved["artist"],
                    "album": get_tag_fallback_global("album", default="Unknown"),
                    "albumartist": get_tag_fallback_global(
                        "albumartist", "album_artist", default=resolved["artist"]
                    ),
                    "tracknumber": str(num),
                    "date": get_tag_fallback_global("date"),
                    "genre": get_tag_fallback_global("genre"),
                    "duration": resolved["duration"],
                    "samplerate": sr,
                    "channels": 1,
                    "cuesheet": cuesheet_payload,
                })
                if "composer" in resolved:
                    track_meta["composer"] = resolved["composer"]

                insert_to_postgres_dummy(
                    audio_hash=mix_hash,
                    filepath=file_path,
                    track_number=num,
                    metadata_tags=track_meta,
                    track_features=track_features,
                    essentia_features=essentia_feats,
                    demucs_features=demucs_feats,
                )
                # メモリ早期解放
                del seg_audio
                gc.collect()
        else:
            track_features, essentia_feats, demucs_feats, mix_hash = (
                analyze_segment_pipeline(audio, sr, essentia_models, MODELS_DIR)
            )

            # 1. FLACタグ用 (mixの特徴量をファイル全体タグとして書き込み)
            mix_lib_feat = track_features.get("mix")
            if mix_lib_feat:
                final_tags.update(mix_lib_feat.to_flac_tags())
            if essentia_feats:
                final_tags.update(essentia_feats.to_flac_tags())
            # NEW: DEMUCS_* タグを追記
            final_tags.update(demucs_feats.to_flac_tags())

            # 2. Postgres INSERT用
            def get_tag_fallback_global(*keys, default=""):
                for k in keys:
                    val = meta.get(k.lower())
                    if val and val[0]:
                        return val[0]
                return default

            # シングルトラックの場合は、トラック個別タグ（cue_trackXX_...）を除外して共通タグのみマージしますの
            track_meta = {}
            for k, v in raw_tags.items():
                if not TRACK_TAG_PAT.match(k):
                    track_meta[k] = v

            # トラック番号の安全なパース
            track_num_str = get_tag_fallback_global("tracknumber", "track")
            track_num_val = None
            if track_num_str:
                track_num_str_clean = track_num_str.split("/")[0].split("-")[0].strip()
                try:
                    track_num_val = int(track_num_str_clean)
                except ValueError:
                    pass

            # 既存の共通解決データで上書きマージ
            track_meta.update({
                "title": get_tag_fallback_global("title", default="Unknown"),
                "artist": get_tag_fallback_global("artist", default="Unknown"),
                "album": get_tag_fallback_global("album", default="Unknown"),
                "albumartist": get_tag_fallback_global(
                    "albumartist",
                    "album_artist",
                    default=get_tag_fallback_global("artist", default="Unknown"),
                ),
                "tracknumber": str(track_num_val) if track_num_val is not None else track_num_str,
                "date": get_tag_fallback_global("date"),
                "genre": get_tag_fallback_global("genre"),
                "duration": meta.info.length,
                "samplerate": meta.info.sample_rate,
                "channels": meta.info.channels,
                "cuesheet": None,
            })

            insert_to_postgres_dummy(
                audio_hash=mix_hash,
                filepath=file_path,
                track_number=track_num_val,
                metadata_tags=track_meta,
                track_features=track_features,
                essentia_features=essentia_feats,
                demucs_features=demucs_feats,
            )

        final_tags = pack_essentia_multi_tags(final_tags)
        write_flac_tags_atomic(file_path, final_tags)
        return f"OK: {basename}  ({len(final_tags)} タグ)"

    except Exception as e:
        logging.exception(f"NG: {basename}")
        return f"NG: {basename}: {e}"
    finally:
        if "audio" in locals():
            del audio
        gc.collect()


# ═══════════════════════════════════════════════════════════
# Producer-Consumer Pipeline (Demucs Full-Throttle)
# ═══════════════════════════════════════════════════════════

import hashlib
import json
import multiprocessing
from concurrent.futures import ThreadPoolExecutor, as_completed


# (Obsolete SHM helper functions removed. load_wave.py is used instead.)


def analyze_stems(
    stem_context,
    essentia_models: dict,
    workers: int = 4,
) -> tuple[dict, Any | None, DemucsFeatures, str]:
    """StemContext を受け取り、DSP warmup → Librosa → Essentia → Post-processing を実行しますわ。
    Producer 側で分離済みのステムを Consumer で解析するために使いますの。"""
    import time
    import multiprocessing
    proc_name = multiprocessing.current_process().name
    mix_hash = stem_context.stems["mix"].audio_hash

    # DSP prewarming (全ステムの遅延プロパティを直列で評価)
    t_start_warmup = time.perf_counter()
    for name, ctx in stem_context.stems.items():
        if name == "mix":
            _ = ctx.stft; _ = ctx.spectro; _ = ctx.power
            _ = ctx.chroma; _ = ctx.mel; _ = ctx.hnr
            _ = ctx.chroma_cqt; _ = ctx.tempobeat
            _ = ctx.onset_env; _ = ctx.tempogram
        elif name in ("drums", "bass"):
            _ = ctx.stft; _ = ctx.spectro; _ = ctx.power
            _ = ctx.hnr; _ = ctx.tempobeat
            _ = ctx.onset_env; _ = ctx.tempogram
        elif name == "vocals":
            _ = ctx.stft; _ = ctx.spectro; _ = ctx.power
            _ = ctx.hnr; _ = ctx.chroma; _ = ctx.mel
        else:
            _ = ctx.stft; _ = ctx.spectro; _ = ctx.power
            _ = ctx.hnr; _ = ctx.chroma
    logging.info(
        f"[{proc_name}] [Endomorphism] [DSP] [Pre-warming] "
        f"全ステム{len(stem_context.stems)}本の遅延キャッシュ事前計算を完了いたしましたわ (経過: {time.perf_counter() - t_start_warmup:.4f}s)"
    )

    # Librosa ThreadPool
    t_start_librosa = time.perf_counter()
    logging.info(
        f"[{proc_name}] [Morphism] [Librosa] [Extraction] "
        f"Librosa による特徴量抽出(ThreadPool={workers})を開始しますわ"
    )
    track_features: dict = {}
    if workers > 1 and len(stem_context.stems) > 1:
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {
                pool.submit(librosa_extractor.run, ctx): name
                for name, ctx in stem_context.stems.items()
            }
            for fut in as_completed(futures):
                track_features[futures[fut]] = fut.result()
    else:
        for name, ctx in stem_context.stems.items():
            track_features[name] = librosa_extractor.run(ctx)
    logging.info(
        f"[{proc_name}] [Morphism] [Librosa] [Extraction] "
        f"全ステムの Librosa 特徴量抽出を完了いたしましたわ (経過: {time.perf_counter() - t_start_librosa:.4f}s)"
    )

    # Post-processing: energy ratios + SNR overwrite
    t_start_snr = time.perf_counter()
    total_stem_energy = sum(
        feat.energy for name, feat in track_features.items() if name != "mix"
    )
    epsilon = 1e-10
    for name, feat in track_features.items():
        if name == "mix":
            continue
        feat.snr = (feat.energy + epsilon) / (total_stem_energy + 2 * epsilon)

    stems_feats = {name: feat for name, feat in track_features.items() if name != "mix"}
    energy_ratios = {
        name: float((feat.energy + epsilon) / (total_stem_energy + 2 * epsilon))
        for name, feat in stems_feats.items()
    }
    demucs_feats = DemucsFeatures(stems=stems_feats, energy_ratios=energy_ratios)
    logging.info(
        f"[{proc_name}] [Endomorphism] [Post-processing] [SNR-Overwrite] "
        f"ステム相対エネルギー比率(SNR)の上書きを完了いたしましたわ (経過: {time.perf_counter() - t_start_snr:.4f}s)"
    )

    # Essentia (mix のみ)
    essentia_feats = None
    if models.GLOBAL_ONNX_SESSIONS.get("effnet") is not None:
        t_start_essentia = time.perf_counter()
        logging.info(
            f"[{proc_name}] [Morphism] [Essentia] [ONNX-Inference] "
            f"Essentia (EffNet) 分類器{len(essentia_models)}基による推論を開始しますわ"
        )
        try:
            mix_ctx = stem_context.stems["mix"]
            patches = models.extract_mel_patches(mix_ctx.y, mix_ctx.sr, n_patches=64)
            preds_dict = models.run_essentia_serialized(patches, essentia_models)
            essentia_feats = EssentiaFeatures(preds_dict)
            logging.info(
                f"[{proc_name}] [Morphism] [Essentia] [ONNX-Inference] "
                f"Essentia 分類推論を完了いたしましたわ (経過: {time.perf_counter() - t_start_essentia:.4f}s)"
            )
        except Exception as e:
            logging.error(f"Essentia 解析エラー: {e}", exc_info=True)

    return track_features, essentia_feats, demucs_feats, mix_hash


def _build_metadata_tags(filepath: str) -> dict:
    """FLAC から最低限の Vorbis comment タグを抽出（Consumer 用簡易版）"""
    from mutagen.flac import FLAC
    meta = FLAC(filepath)
    tags = {}
    for k, v in meta.items():
        val_list = [str(x) for x in v]
        key_lower = k.lower()
        if len(val_list) == 1:
            tags[key_lower] = val_list[0]
        elif val_list:
            tags[key_lower] = val_list
        else:
            tags[key_lower] = ""
    tags.setdefault("title", "Unknown")
    tags.setdefault("artist", "Unknown")
    tags.setdefault("album", "Unknown")
    tags.setdefault("albumartist", "Unknown")
    return tags


def run_producer(
    files: list[str],
    out_queue: multiprocessing.Queue,
    models_dir: str,
    dsn: str | None = None,
    resume: bool = False,
    rough: bool = False,
    shm_dir: str = "",
    enqueued: sharedctypes.Synchronized | None = None,
    completed: sharedctypes.Synchronized | None = None,
    use_dml: bool = False,
):

    """Producer プロセスエントリポイント。
    flac_decode → Demucs → load_wave.save_stems → Queue にメタデータを送信"""
    logging.info(f"[Producer] 起動: {len(files)}ファイル  models_dir={models_dir}  use_dml={use_dml}  rough={rough}")

    models.init_global_demucs(use_dml=use_dml)

    if not shm_dir:
        shm_dir = load_wave.get_default_shm_dir()

    skip_hashes: set[str] = set()
    skip_tracks: set[tuple[str, int]] = set()

    # DBアクセスなし（オーケストレータ側で判定しますわ）

    skipped = 0
    for fp in files:
        try:
            if not os.path.exists(fp):
                continue
            fp_abs = os.path.abspath(fp)
            basename = os.path.basename(fp)

            # mutagenによるメタデータ & CUE境界解析
            flac_handle = flac_decode.build_flac_handle(fp_abs)
            tags = _build_metadata_tags(fp_abs)

            for seg in flac_handle.slices:
                # 1. Roughモードでのスキップチェック
                if rough and (fp_abs, seg.track_number) in skip_tracks:
                    skipped += 1
                    continue

                # 2. 部分デコード (WAVパース、10分以上はストリーミング、44.1kHzにリサンプリング)
                audio_44100, md5_hash = flac_decode.process_slice_with_seq_safety(
                    fp_abs, seg.start_sample, seg.end_sample, flac_handle.sample_rate, flac_handle.channels
                )

                # 3. 通常モードでのハッシュチェック
                if not rough and md5_hash in skip_hashes:
                    del audio_44100
                    skipped += 1
                    continue

                # 4. Demucsによる分離推論 (ダウンサンプリング済みの 44.1kHz で実行)
                stem_ctx = models.GLOBAL_DEMUCS.separate(audio_44100, 44100)
                del audio_44100

                # 5. load_waveによる動的転送モード判定
                transfer_mode = load_wave.get_transfer_mode(
                    fp_abs, 
                    seg.end_sample - seg.start_sample, 
                    flac_handle.channels, 
                    flac_handle.bits_per_sample
                )

                # 6. ステムの退避 (shm または disk)
                transfer_meta = load_wave.save_stems(md5_hash, stem_ctx, transfer_mode, shm_dir)
                stem_ctx.clear()
                del stem_ctx

                # 7. メタデータタグの上書き
                track_tags = tags.copy()
                track_tags["title"] = seg.title
                track_tags["artist"] = seg.artist
                track_tags["tracknumber"] = str(seg.track_number)
                if seg.composer:
                    track_tags["composer"] = seg.composer

                # 8. Queueへの送信
                out_queue.put({
                    "filepath": fp_abs,
                    "track_number": seg.track_number,
                    "transfer_meta": transfer_meta,
                    "metadata_tags": track_tags,
                })
                if enqueued is not None:
                    enqueued.value += 1
                logging.info(f"[Producer] → {basename} (Track: {seg.track_number}, Mode: {transfer_mode})")

        except Exception as e:
            logging.error(f"[Producer] NG: {os.path.basename(fp)}: {e}", exc_info=True)
        finally:
            gc.collect()

    if skipped:
        logging.info(f"[Producer] --resume スキップ: {skipped}曲")

    # Consumer がすべての enqueued タスクを処理し終えるまで待機（SharedMemoryの早期消滅を防ぐため）
    if completed is not None and enqueued is not None:
        logging.info("[Producer] Consumer が全キューを処理し終えるのを待機しますわ...")
        while completed.value < enqueued.value:
            time.sleep(0.5)

    load_wave.clear_producer_shm_cache()
    logging.info("[Producer] 全ファイル処理完了")



def run_consumer(
    in_queue: multiprocessing.Queue,
    models_dir: str,
    dsn: str,
    workers: int = 4,
    shm_dir: str = "",
    completed: sharedctypes.Synchronized | None = None,
):
    """Consumer プロセスエントリポイント。
    Queue からタスクを取得 → load_wave.load_stems → 分析 → PG UPSERT → cleanup"""
    import time

    proc_name = multiprocessing.current_process().name
    logging.info(f"[{proc_name}] Consumer 起動 (workers={workers})")

    if not shm_dir:
        shm_dir = load_wave.get_default_shm_dir()

    essentia_models = models.init_worker_onnx(models_dir)

    while True:
        msg = in_queue.get()
        if msg is None:
            logging.info(f"[{proc_name}] 毒薬受取 → 終了")
            break

        fp = msg["filepath"]
        track_number = msg["track_number"]
        transfer_meta = msg["transfer_meta"]
        metadata_tags = msg["metadata_tags"]
        
        h = transfer_meta["hash"]
        basename = os.path.basename(fp)
        t_start_total = time.perf_counter()
        
        try:
            # 1. 透過的ステムデータロード (load_wave 経由)
            t_shm = time.perf_counter()
            stem_ctx = load_wave.load_stems(transfer_meta, shm_dir)
            logging.info(
                f"[{proc_name}] ステムデータを復元しました (経過: {time.perf_counter() - t_shm:.4f}s, Mode: {transfer_meta['shm_type']})"
            )

            # 2. 解析 (Librosa / Essentia, ステムはすでに 44.1kHz に統一されています)
            # 各 AudioContext に md5_hash を注入してハッシュ一貫性を保証
            for name, ctx in stem_ctx.stems.items():
                ctx._audio_hash = h
                
            track_features, essentia_feats, demucs_feats, mix_hash = analyze_stems(
                stem_ctx, essentia_models, workers
            )
            # 3. 解析結果を JSON Lines として標準出力へダンプいたしますわ
            features_payload = {}
            mix_feat = track_features.get("mix")
            if mix_feat:
                dict_mix = mix_feat.to_postgres_dict(track_id="mix")
                features_payload["mix"] = {
                    "scalars": dict_mix["scalars"],
                    "sequences": dict_mix["sequences"]
                }
            if demucs_feats:
                features_payload["demucs"] = demucs_feats.to_postgres_dict()

            predictions_payload = {}
            if essentia_feats:
                predictions_payload = essentia_feats.to_postgres_dict()

            output_data = {
                "audio_hash": mix_hash,
                "filepath": fp,
                "track_number": track_number,
                "metadata_tags": metadata_tags,
                "features": features_payload,
                "predictions": predictions_payload
            }
            import json
            import sys
            print(json.dumps(output_data, ensure_ascii=False, cls=SafeAudioJSONEncoder))
            sys.stdout.flush()


            if completed is not None:
                completed.value += 1
            logging.info(f"[{proc_name}] OK: {basename} (Track: {track_number}, 全処理経過: {time.perf_counter() - t_start_total:.4f}s)")
        except Exception as e:
            logging.error(f"[{proc_name}] NG: {basename} (Track: {track_number}): {e}", exc_info=True)
        finally:
            # 4. リソースのクリーンアップ
            t_cleanup = time.perf_counter()
            load_wave.cleanup_stems(transfer_meta, shm_dir)
            logging.info(
                f"[{proc_name}] 一時共有リソースを破棄いたしました (経過: {time.perf_counter() - t_cleanup:.4f}s)"
            )

            if "stem_ctx" in locals() and stem_ctx is not None:
                stem_ctx.clear()
                del stem_ctx
            if "track_features" in locals():
                del track_features
            if "essentia_feats" in locals():
                del essentia_feats
            if "demucs_feats" in locals():
                del demucs_feats
            gc.collect()

    conn.close()





def process_single_flac_file_directly(
    filepath: str,
    essentia_models: dict,
    use_dml: bool = False,
) -> str:
    """単一の FLAC ファイルをインプロセスで完全に解析・処理しますわ。
    PowerShell から 1ファイルずつ起動されることを想定し、RAMのリークや断片化を極小化しますの。"""
    import gc
    import time

    basename = os.path.basename(filepath)
    filepath_abs = os.path.abspath(filepath)

    logging.info(f"[Direct-Process] 解析開始: {basename}")
    # 2. FLAC ハンドルの構築 (Cuesheet / メタデータ)
    try:
        flac_handle = flac_decode.build_flac_handle(filepath_abs)
    except Exception as e_handle:
        logging.error(f"[Direct-Process] FLACメタデータ解析失敗: {basename}: {e_handle}")
        return f"NG: Metadata error: {e_handle}"

    # mutagen の全メタデータを辞書化
    tags = _build_metadata_tags(filepath_abs)
    TRACK_TAG_PAT = re.compile(r"^(?:cue_)?track_?(\d+)_(.+)$", re.IGNORECASE)
    final_tags = {}
    processed_tracks = 0
    skipped_tracks_count = 0

    try:
        # Demucs の初期化 (必要に応じてインプロセスでシングルトンとしてロードされますわ)
        models.init_global_demucs(use_dml=use_dml)

        # cuesheet があれば、cuesheet text も payload に含める
        cue_text = None
        for k in flac_handle.tags.keys():
            if k.lower() == "cuesheet":
                cue_text = flac_handle.tags[k]
                if isinstance(cue_text, list):
                    cue_text = cue_text[0]
                break

        resolved_tracks = []
        for seg in flac_handle.slices:
            resolved_tracks.append({
                "track": seg.track_number,
                "title": seg.title,
                "artist": seg.artist,
                "start_sample": seg.start_sample,
                "end_sample": seg.end_sample,
                "duration": float(seg.end_sample - seg.start_sample) / flac_handle.sample_rate,
            })
        cuesheet_payload = {"raw": cue_text, "tracks": resolved_tracks} if cue_text else None

        is_multi_track = len(flac_handle.slices) > 1 or cue_text is not None

        # シングルトラック時のトラック番号抽出
        single_track_num = None
        if not is_multi_track:
            track_num_str = tags.get("tracknumber") or tags.get("track")
            if track_num_str:
                if isinstance(track_num_str, list):
                    track_num_str = track_num_str[0]
                track_num_str_clean = track_num_str.split("/")[0].split("-")[0].strip()
                try:
                    single_track_num = int(track_num_str_clean)
                except ValueError:
                    pass

        for seg in flac_handle.slices:
            num = seg.track_number# 部分デコードとハッシュ計算
            t_dec = time.perf_counter()
            audio_44100, md5_hash = flac_decode.process_slice_with_seq_safety(
                filepath_abs, seg.start_sample, seg.end_sample, flac_handle.sample_rate, flac_handle.channels
            )
            logging.info(
                f"[Direct-Process] デコード完了 (経過: {time.perf_counter() - t_dec:.4f}s, length: {len(audio_44100)} samples)"
            )# スライスが短すぎる場合のガード
            if len(audio_44100) < 100:
                logging.warning(f"[Direct-Process] Track {num} のサンプル数が極端に少ないため解析をスキップしますわ。")
                skipped_tracks_count += 1
                del audio_44100
                continue

            # Demucs 波形分離 (GLOBAL_DEMUCS)
            t_dem = time.perf_counter()
            stem_ctx = models.GLOBAL_DEMUCS.separate(audio_44100, 44100)
            logging.info(
                f"[Direct-Process] Demucs 波形分離完了 (経過: {time.perf_counter() - t_dem:.4f}s)"
            )
            del audio_44100

            # 各ステムへハッシュ注入
            for name, ctx in stem_ctx.stems.items():
                ctx._audio_hash = md5_hash

            # ステム解析 (Librosa + Essentia)
            t_an = time.perf_counter()
            workers_num = get_segment_workers()
            track_features, essentia_feats, demucs_feats, mix_hash = analyze_stems(
                stem_ctx, essentia_models, workers=workers_num
            )
            logging.info(
                f"[Direct-Process] ステム特徴量抽出完了 (経過: {time.perf_counter() - t_an:.4f}s)"
            )

            # FLAC タグの準備
            prefix = f"CUE_TRACK{num:02d}" if is_multi_track else ""

            mix_lib_feat = track_features.get("mix")
            if mix_lib_feat:
                final_tags.update(mix_lib_feat.to_flac_tags(prefix=prefix))
            if essentia_feats:
                ess_tags = essentia_feats.to_flac_tags()
                for k, v in ess_tags.items():
                    key_with_prefix = f"{prefix}_{k}" if prefix else k
                    final_tags[key_with_prefix] = v
            # DEMUCS_* タグを追記
            demucs_tags = demucs_feats.to_flac_tags()
            for k, v in demucs_tags.items():
                key_with_prefix = f"{prefix}_{k}" if prefix else k
                final_tags[key_with_prefix] = v

            # PostgreSQL INSERT用のメタデータ
            track_meta = {}
            for k, v in tags.items():
                m = TRACK_TAG_PAT.match(k)
                if m:
                    tag_track_num = int(m.group(1))
                    tag_name = m.group(2)
                    if tag_track_num == num:
                        track_meta[tag_name] = v
                else:
                    track_meta[k] = v

            # 必須メタデータの上書きマージ
            track_meta.update({
                "title": seg.title if seg.title else f"Track {num}",
                "artist": seg.artist if seg.artist else "Unknown",
                "album": tags.get("album") or "Unknown",
                "albumartist": tags.get("albumartist") or tags.get("album_artist") or seg.artist or "Unknown",
                "tracknumber": str(num),
                "date": tags.get("date"),
                "genre": tags.get("genre"),
                "duration": float(seg.end_sample - seg.start_sample) / flac_handle.sample_rate,
                "samplerate": flac_handle.sample_rate,
                "channels": flac_handle.channels,
                "cuesheet": cuesheet_payload,
            })
            if seg.composer:
                track_meta["composer"] = seg.composer

            target_track_num = num if is_multi_track else single_track_num
            # DB 書き込みの代わりに JSON Lines で stdout へ出力しますわ
            features_payload = {}
            mix_feat = track_features.get("mix")
            if mix_feat:
                dict_mix = mix_feat.to_postgres_dict(track_id="mix")
                features_payload["mix"] = {
                    "scalars": dict_mix["scalars"],
                    "sequences": dict_mix["sequences"]
                }
            if demucs_feats:
                features_payload["demucs"] = demucs_feats.to_postgres_dict()

            predictions_payload = {}
            if essentia_feats:
                predictions_payload = essentia_feats.to_postgres_dict()

            output_data = {
                "audio_hash": mix_hash,
                "filepath": filepath_abs,
                "track_number": target_track_num,
                "metadata_tags": track_meta,
                "features": features_payload,
                "predictions": predictions_payload
            }
            print(json.dumps(output_data, ensure_ascii=False, cls=SafeAudioJSONEncoder))
            sys.stdout.flush()


            # 各ループでのメモリ早期解放
            stem_ctx.clear()
            del stem_ctx
            del track_features
            del essentia_feats
            del demucs_feats
            gc.collect()
            processed_tracks += 1

        if processed_tracks > 0:
            # FLAC タグの書き込み
            final_tags = pack_essentia_multi_tags(final_tags)
            write_flac_tags_atomic(filepath_abs, final_tags)
            logging.info(f"[Direct-Process] OK: {filepath_abs}")

            # プロジェクトルートの flac.done に完了パスを追記しますわ
            project_root = os.path.dirname(os.path.abspath(__file__))
            done_file_path = os.path.join(project_root, "flac.done")
            try:
                with open(done_file_path, "a", encoding="utf-8") as df:
                    df.write(filepath_abs + "\n")
            except Exception as e_df:
                logging.warning(f"[Direct-Process] flac.done への追記に失敗しましたわ: {e_df}")

            return f"OK: {basename} ({processed_tracks} トラック処理完了, {skipped_tracks_count} トラック既処理スキップ)"
        elif skipped_tracks_count > 0:
            logging.info(f"[Direct-Process] OK: {filepath_abs} (All tracks skipped)")

            # すでに全トラックがスキップされた（＝過去に完了している）場合も、flac.done に無ければ追記しておきますの
            project_root = os.path.dirname(os.path.abspath(__file__))
            done_file_path = os.path.join(project_root, "flac.done")
            try:
                already_in = False
                if os.path.exists(done_file_path):
                    with open(done_file_path, "r", encoding="utf-8") as df:
                        content = df.read()
                        if filepath_abs in content:
                            already_in = True
                if not already_in:
                    with open(done_file_path, "a", encoding="utf-8") as df:
                        df.write(filepath_abs + "\n")
            except Exception:
                pass

            return f"SKIPPED: {basename}"
        else:
            return f"NO_TRACKS: {basename}"

    except Exception as e:
        if conn:
            conn.rollback()
        logging.exception(f"[Direct-Process] NG: {basename}")
        return f"NG: {basename}: {e}"
    finally:
        if conn:
            conn.close()
        gc.collect()
