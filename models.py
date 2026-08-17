"""
Models module for FLAC Analyzer & Mood Tagger
=============================================
ONNX推論セッション、ハードウェアロック、および波形分離（HTDemucsSeparator）を管理しますの。
"""

import json
import logging
import os
import re
import sys
import threading
import tomllib
from typing import Any

# Windows環境で .venv 内の nvidia ディレクトリ (nvidia-cublas-cu12, nvidia-cudnn-cu12等) の bin を動的追加
if sys.platform == "win32":
    nvidia_base = os.path.join(sys.prefix, "Lib", "site-packages", "nvidia")
    if os.path.exists(nvidia_base):
        for root, dirs, _ in os.walk(nvidia_base):
            if "bin" in dirs:
                bin_path = os.path.join(root, "bin")
                try:
                    os.add_dll_directory(bin_path)
                except Exception:
                    pass
                os.environ["PATH"] = bin_path + os.pathsep + os.environ.get("PATH", "")

import librosa
import numpy as np
import onnxruntime as ort
import soxr
import demucs_onnx
import demucs_onnx.inference

from constants import CLASS_ALIAS, DEFAULT_CLASS_MAP

# ONNX Runtime のグローバル警告ログをミュート (ScatterND等の警告抑制)
os.environ["ORT_LOGGING_LEVEL"] = "3"

# Load global config
CONFIG = {}
config_path = os.path.join(os.path.dirname(__file__), "config.toml")
try:
    with open(config_path, "rb") as f:
        CONFIG = tomllib.load(f)
except Exception as e:
    logging.warning(f"models.py にて config.toml のロードに失敗いたしましたわ: {e}")

def _get_onnx_opt_level():
    opt_str = CONFIG.get("models", {}).get("graph_optimization_level", "basic").lower()
    if opt_str in ("all", "extended"):
        return ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    elif opt_str in ("basic", "standard"):
        return ort.GraphOptimizationLevel.ORT_ENABLE_BASIC
    return ort.GraphOptimizationLevel.ORT_DISABLE_ALL

def _get_provider_configs(providers_list):
    """Blackwell / CUDA 最適化オプションを内包した provider リストを構築しますわ！"""
    configured_providers = []
    cuda_opts = {
        "device_id": 0,
        "arena_extend_strategy": "kNextPowerOfTwo",
        "cudnn_conv_algo_search": "EXHAUSTIVE",
        "do_copy_in_default_stream": True,
    }
    for p in providers_list:
        if isinstance(p, tuple):
            configured_providers.append(p)
        elif p == "CUDAExecutionProvider":
            configured_providers.append(("CUDAExecutionProvider", cuda_opts))
        else:
            configured_providers.append(p)
    return configured_providers

def _custom_make_session(onnx_path, providers):
    sess_opts = ort.SessionOptions()
    sess_opts.log_severity_level = 3
    sess_opts.intra_op_num_threads = CONFIG.get("models", {}).get("intra_op_num_threads", 1)
    sess_opts.inter_op_num_threads = CONFIG.get("models", {}).get("inter_op_num_threads", 1)
    sess_opts.enable_cpu_mem_arena = False
    sess_opts.enable_mem_pattern = True
    sess_opts.graph_optimization_level = _get_onnx_opt_level()
    configured_providers = _get_provider_configs(list(providers))
    return ort.InferenceSession(str(onnx_path), sess_options=sess_opts, providers=configured_providers)

demucs_onnx.inference._make_session = _custom_make_session

# ONNX 推論直列化のためのグローバルロックとセッション
ONNX_LOCK = threading.Lock()
GLOBAL_ONNX_SESSIONS: dict[str, Any] = {}
GLOBAL_DEMUCS: Any = None


def _onnx_fname_to_key(base: str) -> str:
    return re.split(r"-discogs|_msd|_effnet|_musicnn|_maest", base)[0]


def _load_json_classes(models_dir: str) -> dict[str, list[str]]:
    result: dict[str, list[str]] = {}
    if not os.path.exists(models_dir):
        return result
    for fname in os.listdir(models_dir):
        if not fname.endswith(".json"):
            continue
        try:
            with open(os.path.join(models_dir, fname), "r", encoding="utf-8") as f:
                data = json.load(f)
            if "classes" in data:
                key = _onnx_fname_to_key(fname.replace(".json", ""))
                result[key] = data["classes"]
        except Exception as e:
            logging.warning(f"JSONパースに失敗いたしましたわ: {fname} → {e}")
            continue
    return result


def build_essentia_models(models_dir: str) -> dict[str, dict[str, Any]]:
    import json  # 遅延インポートで依存を整理しますわ

    SKIP = re.compile(r"discogs-effnet-bs64|discogs-maest|_embeddings")
    json_classes = _load_json_classes(models_dir)
    models: dict[str, dict[str, Any]] = {}
    if not os.path.exists(models_dir):
        return models

    for fname in sorted(os.listdir(models_dir)):
        if not fname.endswith(".onnx") or SKIP.search(fname):
            continue
        key = _onnx_fname_to_key(fname.replace(".onnx", ""))
        classes: list[str] | None = None
        if key in json_classes:
            classes = json_classes[key]
        else:
            classes = DEFAULT_CLASS_MAP.get(key)
            continue
        if classes is None:
            continue
        backend = "musicnn" if "musicnn" in fname else "effnet"
        models[key] = {
            "file": fname,
            "classes": classes,
            "backend": backend,
        }
        logging.debug(f"  分類器登録: {key:30s}  クラス={classes}")
    return models


def init_global_onnx_sessions(models_dir: str, essentia_models: dict):
    """グローバルにONNXセッションを1セット構築し、直列に使い回しますの。"""
    global GLOBAL_ONNX_SESSIONS
    if not os.path.exists(models_dir):
        logging.warning(
            f"モデルディレクトリ {models_dir} が存在しないため、ONNXは無効化されますわ。"
        )
        return

    available = ort.get_available_providers()
    if "CUDAExecutionProvider" in available:
        providers = ["CUDAExecutionProvider", "CPUExecutionProvider"]
    elif "DmlExecutionProvider" in available:
        providers = ["DmlExecutionProvider", "CPUExecutionProvider"]
    elif "ROCMExecutionProvider" in available:
        providers = ["ROCMExecutionProvider", "CPUExecutionProvider"]
    else:
        providers = ["CPUExecutionProvider"]

    logging.info(f"ONNX使用可能演算器: {available}")
    logging.info(f"直列実行用ロード     : {providers}")

    opts = ort.SessionOptions()
    opts.log_severity_level = 3
    opts.intra_op_num_threads = CONFIG.get("models", {}).get("intra_op_num_threads", 1)  # セグフォ防止
    opts.inter_op_num_threads = CONFIG.get("models", {}).get("inter_op_num_threads", 1)
    opts.enable_cpu_mem_arena = False  # OOM防止
    opts.enable_mem_pattern = True
    opts.graph_optimization_level = _get_onnx_opt_level()

    configured_providers = _get_provider_configs(providers)

    effnet_path = os.path.join(models_dir, "discogs-effnet-bs64-1.onnx")
    effnet_sess = eff_in = eff_out = None
    if os.path.exists(effnet_path):
        effnet_sess = ort.InferenceSession(effnet_path, opts, providers=configured_providers)
        eff_in = effnet_sess.get_inputs()[0].name
        eff_out = effnet_sess.get_outputs()[0].name

    classifiers: dict[str, ort.InferenceSession] = {}
    for key, info in essentia_models.items():
        m_path = os.path.join(models_dir, info["file"])
        if os.path.exists(m_path):
            classifiers[key] = ort.InferenceSession(m_path, opts, providers=configured_providers)

    GLOBAL_ONNX_SESSIONS = {
        "effnet": effnet_sess,
        "eff_in": eff_in,
        "eff_out": eff_out,
        "classifiers": classifiers,
    }
    logging.info(f"ONNXセッション直列化ロード完了！ (分類器数: {len(classifiers)})")


def extract_mel_patches(audio: np.ndarray, sr: int, n_patches: int = 64) -> np.ndarray:
    """[Delegation] Essentia EffNet Mel パッチ計測器 (analyzer.essentia_dsp へ委譲)"""
    from analyzer.essentia_dsp import extract_mel_patches as _emp
    resample_sr = CONFIG.get("models", {}).get("resample_sr", 16000)
    n_fft = CONFIG.get("models", {}).get("n_fft", 512)
    hop_length = CONFIG.get("models", {}).get("hop_length", 256)
    n_mels = CONFIG.get("models", {}).get("n_mels", 96)
    patch_size = CONFIG.get("models", {}).get("patch_size", 128)
    patch_hop = CONFIG.get("models", {}).get("patch_hop", 62)
    return _emp(
        audio=audio,
        sr=sr,
        n_patches=n_patches,
        resample_sr=resample_sr,
        n_fft=n_fft,
        hop_length=hop_length,
        n_mels=n_mels,
        patch_size=patch_size,
        patch_hop=patch_hop,
    )


def run_essentia_serialized(
    patches: np.ndarray, essentia_models: dict
) -> dict[str, float]:
    """[Delegation] Essentia ONNX 推論計測器 (analyzer.essentia_dsp へ委譲)"""
    from analyzer.essentia_dsp import run_essentia_serialized as _res
    return _res(
        patches=patches,
        essentia_models=essentia_models,
        sessions=GLOBAL_ONNX_SESSIONS,
        lock=ONNX_LOCK,
    )


# DummyDemucsSeparator has been removed per user request for fail-fast behavior


class HTDemucsSeparator:
    """HTDemucs ONNX 実機モデルを用いた波形分離器ですわ！"""

    def __init__(self, model_name: str = "htdemucs_6s", precision: str = "fp16weights", use_dml: bool = False):
        import demucs_onnx.inference as inf
        self.model_name = model_name
        self.precision = precision
        available = ort.get_available_providers()
        self.providers = []
        for p in available:
            if p in ["CUDAExecutionProvider", "ROCmExecutionProvider"] or (use_dml and p == "DmlExecutionProvider"):
                self.providers.append(p)
        self.providers.append("CPUExecutionProvider")

        logging.info(f"HTDemucsSeparator 初期化: model={model_name}, precision={precision}, providers={self.providers}")

        # モデルの解決とONNXセッションの事前構築を行いますわ
        self.canonical = inf.resolve_model_name(model_name)
        if self.canonical in inf.MODEL_REGISTRY and inf.MODEL_REGISTRY[self.canonical].kind == "single":
            self.model_info = inf.MODEL_REGISTRY[self.canonical]
            import os
            import glob
            cache_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "demucs")
            user_hf_cache = os.path.expanduser("~/.cache/huggingface/hub")
            os.makedirs(cache_dir, exist_ok=True)

            target_filename = f"{self.canonical}_{precision}.onnx" if precision != "fp32" else f"{self.canonical}.onnx"
            # precision "fp16weights" の場合のファイル名補正
            if precision == "fp16weights":
                target_filename = "htdemucs_6s_fp16weights.onnx"

            search_patterns = [
                os.path.join(cache_dir, "models--StemSplitio--htdemucs-6s-onnx", "snapshots", "*", target_filename),
                os.path.join(user_hf_cache, "models--StemSplitio--htdemucs-6s-onnx", "snapshots", "*", target_filename),
                os.path.join(cache_dir, "models--StemSplitio--htdemucs-6s-onnx", "snapshots", "*", "*.onnx"),
                os.path.join(user_hf_cache, "models--StemSplitio--htdemucs-6s-onnx", "snapshots", "*", "*.onnx"),
            ]

            found_model = None
            for pattern in search_patterns:
                matches = glob.glob(pattern)
                for m in matches:
                    if os.path.exists(m) and os.path.getsize(m) > 10 * 1024 * 1024:
                        found_model = m
                        break
                if found_model:
                    break

            # snapshots にない場合、blobs 配下の大容量ファイルを最後の手段として探索しますの
            if not found_model:
                blob_patterns = [
                    os.path.join(cache_dir, "models--StemSplitio--htdemucs-6s-onnx", "blobs", "*"),
                    os.path.join(user_hf_cache, "models--StemSplitio--htdemucs-6s-onnx", "blobs", "*"),
                ]
                for bp in blob_patterns:
                    for bm in glob.glob(bp):
                        if os.path.isfile(bm) and os.path.getsize(bm) > 100 * 1024 * 1024:
                            found_model = bm
                            break
                    if found_model:
                        break

            if found_model:
                self.model_path = found_model
                logging.info(f"ローカルキャッシュから Demucs ONNX モデルを直接ロードしますの: {self.model_path}")
            else:
                logging.info("キャッシュモデルが見つからないため、Hugging Face Hub からダウンロードを試みますわ...")
                orig_offline = os.environ.pop("HF_HUB_OFFLINE", None)
                try:
                    self.model_path = inf.download_single_model(
                        self.canonical, precision=precision, cache_dir=cache_dir
                    )
                finally:
                    if orig_offline is not None:
                        os.environ["HF_HUB_OFFLINE"] = orig_offline
            # ONNXセッションの構築 (カスタム作成フックを経由)
            self.session = _custom_make_session(self.model_path, self.providers)
        else:
            raise ValueError(f"モデル {model_name} は単一ONNX推論に対応していませんわ。")

    def separate(self, y: np.ndarray, sr: int) -> Any:
        from analyzer import AudioContext, StemContext
        import demucs_onnx.inference as inf

        try:
            # 2. 入力波形をステレオ (2, N) のチャンネル・ファーストに整形
            audio_in = y
            if audio_in.ndim == 1:
                audio_in = np.tile(audio_in, (2, 1))
            elif audio_in.shape[0] != 2:
                audio_in = audio_in.T
                if audio_in.shape[0] == 1:
                    audio_in = np.tile(audio_in, (2, 1))
                elif audio_in.shape[0] > 2:
                    audio_in = audio_in[:2]
            
            # Demucsの要求する 44100Hz にリサンプリング
            if sr != 44100:
                audio_in = soxr.resample(audio_in.T, float(sr), 44100.0).T

            audio_in = np.ascontiguousarray(audio_in, dtype=np.float32)

            # 混合ソース (mix) は 44.1kHz モノラルとして stems に登録 (アップサンプリングによるメモリ爆発の完全防止)
            mix_mono = np.mean(audio_in, axis=0)
            stems = {"mix": AudioContext(mix_mono, 44100, "mix")}

            logging.info(f"[HTDemucs ONNX Memory] 推論処理を開始しますわ... (ONNX_LOCK 同期)")
            with ONNX_LOCK:
                # 一時WAVファイルを経由せず、直接オンメモリ推論を実行いたしますの！
                separated_stems = inf._chunked_separate_single(
                    session=self.session,
                    sources=self.model_info.sources,
                    mix=audio_in,
                    verbose=False,
                    progress=False,
                )

            logging.info(f"[HTDemucs ONNX Memory] 分離完了いたしましたわ！ 整合化を開始しますの。")

            # 3. 得られた各ステムの波形データを整合化 (モノラル化、44.1kHz のまま保持)
            for name, stem_y in separated_stems.items():
                # ステレオ (2, N) からモノラルへの平均化
                if stem_y.ndim > 1:
                    stem_y = np.mean(stem_y, axis=0)
                
                # 逆リサンプリング(44100Hz -> sr)は廃止し、44.1kHzのまま保持
                stems[name] = AudioContext(stem_y, 44100, name)

        except Exception as e:
            logging.error(f"[ERROR] [HTDemucs ONNX Memory] 分離実行中に深刻なエラーが発生いたしましたわ (OOM/Type等): {e}", exc_info=True)
            # エラー時はダミーフォールバックせず、そのまま例外を投げてプロセスを異常終了させますの（Fail Fast）
            raise RuntimeError(f"Demucs separation failed for track: {e}")

        return StemContext(stems)


def init_global_demucs(use_dml: bool = False):
    global GLOBAL_DEMUCS
    logging.info(f"波形分離モデル (GLOBAL_DEMUCS) をロードしますわ... (use_dml={use_dml})")
    try:
        GLOBAL_DEMUCS = HTDemucsSeparator(model_name="htdemucs_6s", precision="fp16weights", use_dml=use_dml)
        logging.info("HTDemucs ONNX 実機モデルロードに成功いたしましたわ！")
    except Exception as e:
        logging.error(f"[ERROR] HTDemucs ONNX 実機モデルロード失敗いたしましたわ: {e}", exc_info=True)
        # フォールバック廃止のため、ここでも例外を投げてプロセスを終了させます
        raise RuntimeError(f"Failed to load global demucs model: {e}")


def init_worker_onnx(models_dir: str) -> dict:
    """子プロセス (Consumer) 内で ONNX セッションを初期化しますわ。
    親プロセスで開いたセッションは fork 非対応なので、spawn した子で改めて開き直す必要がありますの。
    Returns: essentia_models dict (分類器定義)"""
    import logging
    essentia_models = build_essentia_models(models_dir)
    init_global_onnx_sessions(models_dir, essentia_models)
    logging.info(f"[WorkerONNX] Consumer 内 ONNX 直列セッション初期化完了！（分類器数: {len(essentia_models)})")
    return essentia_models
