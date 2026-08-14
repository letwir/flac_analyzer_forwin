"""
Analyzer Core Module
====================
AudioContext, StemContext, FeatureExtractor (Reader Applicative)
および同期ロック、共通フレーム定数を定義しますの。
"""

import hashlib
import logging
import math
import threading
from dataclasses import dataclass
from typing import Any, Callable, Generic, TypeVar

import librosa
import numpy as np

T = TypeVar("T")
U = TypeVar("U")

# 再入可能なロック（RLock）にすることで、同一スレッド内での二重ロックによる自己デッドロックを防ぎますわ！
LIBROSA_LOCK = threading.RLock()

# ─────────────────────────────────────────────
# 固定フレーム定数（Tonnetz・時系列特徴量の共通定数）
# ─────────────────────────────────────────────
FIXED_SEQ_FRAMES: int = 32
TONNETZ_N_FRAMES: int = FIXED_SEQ_FRAMES  # 後方互換エイリアス


def _resample_to_fixed_frames(
    seq: np.ndarray, n: int = FIXED_SEQ_FRAMES
) -> list[float]:
    """任意長の1D時系列を FIXED_SEQ_FRAMES 点に線形補間しますの（Tonnetz と同一方式）。"""
    length = len(seq)
    if length == 0:
        return [0.0] * n
    x_new = np.linspace(0, 1, n)
    x_old = np.linspace(0, 1, length)
    return np.interp(x_new, x_old, seq).tolist()


# ─────────────────────────────────────────────
# AudioContext
# ─────────────────────────────────────────────
class AudioContext:
    """Librosa解析への入力コンテキスト。波形 Tensor (y) とサンプリングレート (sr)、およびソース名 (source) を保持。
    共通部分式除去 (CSE) のため、遅延プロパティキャッシュを実装しておりますわ！
    """

    def __init__(
        self,
        y: np.ndarray,
        sr: int,
        source: str = "mix",
        snr: float | None = None,
        audio_hash: str | None = None,
        spectro_path: str | None = None,
    ):
        # 多次元波形（ステレオ等）の場合は、チャンネル次元を平均化してモノラル（1次元）にするの
        if y.ndim > 1:
            if y.shape[0] == 2:  # channels-first (e.g. from Demucs)
                y = np.mean(y, axis=0)
            elif y.shape[-1] == 2:  # channels-last (e.g. from soundfile)
                y = np.mean(y, axis=-1)
            else:
                y = np.mean(y, axis=0)  # フォールバック

        self.y = np.ascontiguousarray(y, dtype=np.float32)
        self.sr = sr
        self.source = source
        self._snr_val = snr
        self._spectro_path = spectro_path
        # キャッシュバッファ
        self._stft: np.ndarray | None = None
        self._spectro: np.ndarray | None = None
        self._power: np.ndarray | None = None
        self._mel: np.ndarray | None = None
        self._chroma: np.ndarray | None = None
        self._tempobeat: tuple[float, np.ndarray] | None = None
        self._hnr: float | None = None
        self._nap: float | None = None
        self._hnr_db: float | None = None
        self._audio_hash: str | None = audio_hash
        self._chroma_cqt: np.ndarray | None = None
        self._onset_env: np.ndarray | None = None  # onset strength envelope
        self._tempogram: np.ndarray | None = None  # tempogram cache
        self._centroid: np.ndarray | None = None  # spectral centroid cache

    @property
    def audio_hash(self) -> str:
        if self._audio_hash is None:
            logging.debug(
                f"    [CSE Cache Miss] audio_hash 計算開始 (source: {self.source})"
            )
            m = hashlib.md5()
            m.update(self.y.tobytes())
            self._audio_hash = m.hexdigest()
        else:
            logging.debug(
                f"    [CSE Cache Hit] audio_hash 再利用 (source: {self.source})"
            )
        return self._audio_hash

    @property
    def stft(self) -> np.ndarray:
        if self._stft is None:
            logging.debug(f"    [CSE Cache Miss] stft 計算開始 (source: {self.source})")
            with LIBROSA_LOCK:
                self._stft = librosa.stft(
                    self.y.astype(np.float32, copy=False),
                    n_fft=2048,
                    hop_length=512,
                    dtype=np.complex64,
                )
        else:
            logging.debug(f"    [CSE Cache Hit] stft 再利用 (source: {self.source})")
        return self._stft

    @property
    def spectro(self) -> np.ndarray:
        if self._spectro is None:
            if self._spectro_path is not None:
                logging.debug(f"    [CSE Cache Hit] spectro ディスクキャッシュロード (source: {self.source})")
                import os
                if os.path.exists(self._spectro_path):
                    self._spectro = np.load(self._spectro_path, mmap_mode='r')
                else:
                    self._spectro = np.abs(self.stft).astype(np.float32, copy=False)
                    self._stft = None
            else:
                self._spectro = np.abs(self.stft).astype(np.float32, copy=False)
                self._stft = None
        return self._spectro

    @property
    def power(self) -> np.ndarray:
        if self._power is None:
            self._power = np.square(self.spectro, out=self.spectro.copy())
        return self._power

    @property
    def mel(self) -> np.ndarray:
        if self._mel is None:
            logging.debug(f"    [CSE Cache Miss] mel 計算開始 (source: {self.source})")
            with LIBROSA_LOCK:
                self._mel = librosa.feature.melspectrogram(
                    S=self.power, sr=self.sr, n_mels=128
                )
        else:
            logging.debug(f"    [CSE Cache Hit] mel 再利用 (source: {self.source})")
        return self._mel

    @property
    def chroma(self) -> np.ndarray:
        if self._chroma is None:
            logging.debug(
                f"    [CSE Cache Miss] chroma 計算開始 (source: {self.source})"
            )
            with LIBROSA_LOCK:
                self._chroma = librosa.feature.chroma_stft(S=self.spectro, sr=self.sr)
        else:
            logging.debug(f"    [CSE Cache Hit] chroma 再利用 (source: {self.source})")
        return self._chroma

    @property
    def chroma_cqt(self) -> np.ndarray:
        if self._chroma_cqt is None:
            logging.debug(
                f"    [CSE Cache Miss] chroma_cqt 計算開始 (source: {self.source})"
            )
            with LIBROSA_LOCK:
                self._chroma_cqt = librosa.feature.chroma_cqt(y=self.y, sr=self.sr)
        else:
            logging.debug(
                f"    [CSE Cache Hit] chroma_cqt 再利用 (source: {self.source})"
            )
        return self._chroma_cqt

    @property
    def tempobeat(self) -> tuple[float, np.ndarray]:
        if self._tempobeat is None:
            logging.debug(
                f"    [CSE Cache Miss] beat_track 計算開始 (source: {self.source})"
            )
            try:
                with LIBROSA_LOCK:
                    bpm, beats = librosa.beat.beat_track(onset_envelope=self.onset_env, sr=self.sr)
                bpm_val = float(bpm[0] if isinstance(bpm, np.ndarray) else bpm)

                if math.isnan(bpm_val) or math.isinf(bpm_val):
                    bpm_val = 0.0
            except Exception as e:
                logging.exception(f"beat_track 計算に失敗いたしましたわ (source: {self.source}): {e}")
                bpm_val = 0.0
                beats = np.array([], dtype=int)
            self._tempobeat = (bpm_val, beats)
        else:
            logging.debug(
                f"    [CSE Cache Hit] beat_track 再利用 (source: {self.source})"
            )
        return self._tempobeat

    @property
    def centroid(self) -> np.ndarray:
        """Spectral Centroidのキャッシュプロパティですわ。float32で高速計算いたします。"""
        if self._centroid is None:
            logging.debug(
                f"    [CSE Cache Miss] centroid 計算開始 (source: {self.source})"
            )
            freqs = librosa.fft_frequencies(sr=self.sr, n_fft=2048)[:, np.newaxis].astype(np.float32)
            spectro_sum = np.sum(self.spectro, axis=0, keepdims=True)
            spectro_sum = np.where(spectro_sum == 0.0, 1.0, spectro_sum)
            raw_centroid = (np.sum(freqs * self.spectro, axis=0, keepdims=True) / spectro_sum)[0]
            self._centroid = np.nan_to_num(
                raw_centroid, nan=0.0, posinf=0.0, neginf=0.0
            )
        else:
            logging.debug(
                f"    [CSE Cache Hit] centroid 再利用 (source: {self.source})"
            )
        return self._centroid

    @property
    def onset_env(self) -> np.ndarray:
        """Onset Strength Envelope。melキャッシュを再利用して計算しますの。"""
        if self._onset_env is None:
            logging.debug(
                f"    [CSE Cache Miss] onset_env 計算開始 (source: {self.source})"
            )
            with LIBROSA_LOCK:
                mel_max = (
                    np.max(self.mel)
                    if self.mel is not None and self.mel.size > 0
                    else 0.0
                )
                ref_val = float(mel_max) if mel_max > 1e-10 else 1.0
                log_mel = librosa.power_to_db(self.mel, ref=ref_val)
                raw_onset = librosa.onset.onset_strength(S=log_mel, sr=self.sr)
            self._onset_env = np.nan_to_num(raw_onset, nan=0.0, posinf=0.0, neginf=0.0)
        else:
            logging.debug(
                f"    [CSE Cache Hit] onset_env 再利用 (source: {self.source})"
            )
        return self._onset_env

    @property
    def tempogram(self) -> np.ndarray:
        if self._tempogram is None:
            logging.debug(
                f"    [CSE Cache Miss] tempogram 計算開始 (source: {self.source})"
            )
            with LIBROSA_LOCK:
                raw_tempogram = librosa.feature.tempogram(
                    onset_envelope=self.onset_env, sr=self.sr, hop_length=512
                )
            self._tempogram = np.nan_to_num(
                raw_tempogram, nan=0.0, posinf=0.0, neginf=0.0
            )
        else:
            logging.debug(
                f"    [CSE Cache Hit] tempogram 再利用 (source: {self.source})"
            )
        return self._tempogram

    @property
    def nap(self) -> float:
        """Wiener-Khinchin 定理に基づく正規化自己相関ピーク (NAP, 0.0〜1.0) を算出・キャッシュしますわ！"""
        if self._nap is None:
            logging.debug(f"    [CSE Cache Miss] nap 計算開始 (source: {self.source})")
            from .librosa_dsp import _calc_hnr_nap
            self._nap = _calc_hnr_nap(self)
        else:
            logging.debug(f"    [CSE Cache Hit] nap 再利用 (source: {self.source})")
        return self._nap

    @property
    def hnr_db(self) -> float:
        """調波対雑音比 (HNR) をデシベル (dB) スケール (-40.0〜+40.0 dB) で算出・キャッシュしますわ！"""
        if self._hnr_db is None:
            logging.debug(f"    [CSE Cache Miss] hnr_db 計算開始 (source: {self.source})")
            from .librosa_dsp import _calc_hnr_db
            self._hnr_db = _calc_hnr_db(self.nap)
        else:
            logging.debug(f"    [CSE Cache Hit] hnr_db 再利用 (source: {self.source})")
        return self._hnr_db

    @property
    def hnr(self) -> float:
        """後方互換性のためのプロパティですわ (真の dB 値 hnr_db を返却します)"""
        return self.hnr_db

    def clear(self):
        """メモリを早期解放するために、保持している配列の参照をすべて破棄しますわ！"""
        self.y = None
        self._stft = None
        self._spectro = None
        self._power = None
        self._mel = None
        self._chroma = None
        self._tempobeat = None
        self._hnr = None
        self._nap = None
        self._hnr_db = None
        self._chroma_cqt = None
        self._onset_env = None
        self._tempogram = None
        self._centroid = None


# ─────────────────────────────────────────────
# StemContext
# ─────────────────────────────────────────────
@dataclass
class StemContext:
    """各ソースごとの AudioContext をラップするコンテキストデータクラスですわ。"""

    stems: dict[str, AudioContext]

    def clear(self):
        """内包するすべての AudioContext のメモリを解放しますわ！"""
        for ctx in list(self.stems.values()):
            ctx.clear()
        self.stems.clear()


# ─────────────────────────────────────────────
# FeatureExtractor (Reader Applicative)
# ─────────────────────────────────────────────
class FeatureExtractor(Generic[T]):
    """圏論における Reader Applicative に相当する特徴量抽出器ですわ！"""

    def __init__(self, run: Callable[[AudioContext], T], name: str = "extractor"):
        self._run_fn = run
        self.name = name

    def run(self, ctx: AudioContext) -> T:
        logging.debug(f"  [Applicative] {self.name} 開始 (source: {ctx.source})")
        res = self._run_fn(ctx)
        logging.debug(f"  [Applicative] {self.name} 完了 (source: {ctx.source})")
        return res

    @classmethod
    def pure(cls, value: T, name: str = "pure") -> "FeatureExtractor[T]":
        return cls(lambda _: value, name)

    def map(self, f: Callable[[T], U]) -> "FeatureExtractor[U]":
        return FeatureExtractor(lambda ctx: f(self.run(ctx)), f"{self.name}.map")

    def ap(self, f_app: "FeatureExtractor[Callable[[T], U]]") -> "FeatureExtractor[U]":
        return FeatureExtractor(
            lambda ctx: f_app.run(ctx)(self.run(ctx)), f"{self.name}.ap({f_app.name})"
        )

    def __mul__(self, other: "FeatureExtractor[U]") -> "FeatureExtractor[tuple[T, U]]":
        """Product (直積) 演算子 `*` ですわ！"""
        return FeatureExtractor(
            lambda ctx: (self.run(ctx), other.run(ctx)), f"({self.name} * {other.name})"
        )


def product_all(*extractors: FeatureExtractor) -> FeatureExtractor[tuple]:
    """可変長 Product コンビネータですわ！"""
    names = ", ".join(ext.name for ext in extractors)
    return FeatureExtractor(
        lambda ctx: tuple(ext.run(ctx) for ext in extractors), f"Product[{names}]"
    )
