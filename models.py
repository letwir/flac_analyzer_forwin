"""
Models module for FLAC Analyzer & Mood Tagger
=============================================
ONNX推論セッション、ハードウェアロック、および波形分離（HTDemucsSeparator）を管理しますの。
"""

import json
import logging
import os
import re
import threading
import tomllib
from typing import Any

import librosa
import numpy as np
import onnxruntime as ort
import soxr
import demucs_onnx
import demucs_onnx.inference

from constants import CLASS_ALIAS, DEFAULT_CLASS_MAP

# ONNX Runtime のグローバル警告ログをミュート (ScatterND等の警告抑制)
ort.set_default_logger_severity(3)

# Load global config
CONFIG = {}
config_path = os.path.join(os.path.dirname(__file__), "config.toml")
try:
    with open(config_path, "rb") as f:
        CONFIG = tomllib.load(f)
except Exception as e:
    logging.warning(f"Failed to load config.toml in models.py: {e}")

# ─────────────────────────────────────────────
# demucs-onnx のセッション作成フック (モンキーパッチ)
# ─────────────────────────────────────────────
def _custom_make_session(onnx_path, providers):
    sess_opts = ort.SessionOptions()
    sess_opts.log_severity_level = 3
    sess_opts.intra_op_num_threads = CONFIG.get("models", {}).get("intra_op_num_threads", 1)
    sess_opts.inter_op_num_threads = CONFIG.get("models", {}).get("inter_op_num_threads", 1)
    sess_opts.enable_cpu_mem_arena = False
    sess_opts.enable_mem_pattern = False
    sess_opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_DISABLE_ALL
    return ort.InferenceSession(str(onnx_path), sess_options=sess_opts, providers=list(providers))

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
            logging.warning(f"JSONパース失敗: {fname} → {e}")
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
    opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_DISABLE_ALL

    effnet_path = os.path.join(models_dir, "discogs-effnet-bs64-1.onnx")
    effnet_sess = eff_in = eff_out = None
    if os.path.exists(effnet_path):
        effnet_sess = ort.InferenceSession(effnet_path, opts, providers=providers)
        eff_in = effnet_sess.get_inputs()[0].name
        eff_out = effnet_sess.get_outputs()[0].name

    classifiers: dict[str, ort.InferenceSession] = {}
    for key, info in essentia_models.items():
        m_path = os.path.join(models_dir, info["file"])
        if os.path.exists(m_path):
            classifiers[key] = ort.InferenceSession(m_path, opts, providers=providers)

    GLOBAL_ONNX_SESSIONS = {
        "effnet": effnet_sess,
        "eff_in": eff_in,
        "eff_out": eff_out,
        "classifiers": classifiers,
    }
    logging.info(f"ONNXセッション直列化ロード完了！ (分類器数: {len(classifiers)})")


def _resample_to_16k(audio: np.ndarray, sr: int) -> np.ndarray:
    target_sr = CONFIG.get("models", {}).get("resample_sr", 16000)
    return audio if sr == target_sr else soxr.resample(audio, sr, target_sr)


def extract_mel_patches(audio: np.ndarray, sr: int, n_patches: int = 64) -> np.ndarray:
    # 多次元波形（ステレオ等）の場合は、チャンネル次元を平均化してモノラル（1次元）にするの
    if audio.ndim > 1:
        if audio.shape[0] == 2:      # channels-first
            audio = np.mean(audio, axis=0)
        elif audio.shape[-1] == 2:   # channels-last
            audio = np.mean(audio, axis=-1)
        else:
            audio = np.mean(audio, axis=0)  # フォールバック

    target_sr = CONFIG.get("models", {}).get("resample_sr", 16000)
    n_fft = CONFIG.get("models", {}).get("n_fft", 512)
    hop_length = CONFIG.get("models", {}).get("hop_length", 256)
    n_mels = CONFIG.get("models", {}).get("n_mels", 96)
    
    audio_16k = _resample_to_16k(audio, sr)
    mel = librosa.feature.melspectrogram(
        y=audio_16k,
        sr=target_sr,
        n_fft=n_fft,
        hop_length=hop_length,
        n_mels=n_mels,
        power=2.0,
    )
    log_mel = np.log10(10000.0 * mel + 1.0).T

    patch_size = CONFIG.get("models", {}).get("patch_size", 128)
    patch_hop = CONFIG.get("models", {}).get("patch_hop", 62)

    if log_mel.shape[0] < patch_size:
        log_mel = np.pad(log_mel, ((0, patch_size - log_mel.shape[0]), (0, 0)))

    idxs = range(0, log_mel.shape[0] - patch_size + 1, patch_hop)
    raw = np.stack(
        [log_mel[i : i + patch_size] for i in idxs] or [log_mel[:patch_size]]
    )
    M = len(raw)

    if M < n_patches:
        raw = np.tile(raw, ((n_patches // M) + 1, 1, 1))
        raw = raw[:n_patches]
    elif M > n_patches:
        idx = np.linspace(0, M - 1, n_patches, dtype=int)
        raw = raw[idx]

    return raw.astype(np.float32)


def run_essentia_serialized(
    patches: np.ndarray, essentia_models: dict
) -> dict[str, float]:
    """ONNX(Essentia)による分類結果の生確率 dict を直列実行で抽出しますの。"""
    predictions: dict[str, float] = {}
    effnet = GLOBAL_ONNX_SESSIONS.get("effnet")
    if effnet is None:
        return predictions

    with ONNX_LOCK:
        try:
            embeddings = effnet.run(
                [GLOBAL_ONNX_SESSIONS["eff_out"]],
                {GLOBAL_ONNX_SESSIONS["eff_in"]: patches},
            )[0]
            embeddings = np.asarray(embeddings, dtype=np.float32)
            emb_mean = embeddings.mean(axis=0).astype(np.float32)
            emb_2d = emb_mean.reshape(1, -1)
        except Exception as e:
            logging.error(f"effnet backbone エラー: {e}", exc_info=True)
            return predictions

        for key, clf_sess in GLOBAL_ONNX_SESSIONS["classifiers"].items():
            if key not in essentia_models:
                continue
            classes = essentia_models[key]["classes"]
            try:
                clf_input = clf_sess.get_inputs()[0]
                clf_output = clf_sess.get_outputs()[0]
                clf_in_name = clf_input.name
                clf_out_name = clf_output.name
                clf_shape = clf_input.shape

                if len(clf_shape) == 1:
                    inp = emb_mean
                else:
                    batch_dim = clf_shape[0]
                    if isinstance(batch_dim, int) and batch_dim > 1:
                        n = embeddings.shape[0]
                        if n < batch_dim:
                            inp = np.tile(embeddings, ((batch_dim // n) + 1, 1))[
                                :batch_dim
                            ]
                        else:
                            idx = np.linspace(0, n - 1, batch_dim, dtype=int)
                            inp = embeddings[idx]
                        inp = inp.astype(np.float32)
                    else:
                        inp = emb_2d

                preds = clf_sess.run([clf_out_name], {clf_in_name: inp})[0]
                preds = np.asarray(preds)

                if preds.ndim > 1:
                    prob = preds.mean(axis=0)
                else:
                    prob = preds

                for i, cls_name in enumerate(classes):
                    cls_name = CLASS_ALIAS.get(cls_name, cls_name)
                    if len(cls_name) <= 3:
                        continue
                    safe = re.sub(r"[^a-zA-Z0-9_]", "_", cls_name).upper()
                    predictions[f"ESSENTIA_{key.upper()}_{safe}"] = float(prob[i])

            except Exception as e:
                logging.error(f"分類器 [{key}] エラー: {e}", exc_info=True)

    return predictions


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
            cache_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "demucs")
            os.makedirs(cache_dir, exist_ok=True)
            self.model_path = inf.download_single_model(
                self.canonical, precision=precision, cache_dir=cache_dir
            )
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
