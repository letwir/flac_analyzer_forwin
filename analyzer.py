"""
Analyzer module for FLAC Analyzer
=================================
Reader Applicative 抽象化を用いた特徴量抽出器およびデータ構造を定義しますの。
"""

import hashlib
import logging
import threading
from dataclasses import dataclass, field
from typing import Any, Callable, Generic, TypeVar, cast

import librosa
import numpy as np

from constants import CHORDS_DIC, KEY_PROFILES, NOTES

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
        self, y: np.ndarray, sr: int, source: str = "mix", snr: float | None = None, audio_hash: str | None = None
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
        # キャッシュバッファ
        self._stft: np.ndarray | None = None
        self._spectro: np.ndarray | None = None
        self._power: np.ndarray | None = None
        self._mel: np.ndarray | None = None
        self._chroma: np.ndarray | None = None
        self._tempobeat: tuple[float, np.ndarray] | None = None
        self._hnr: float | None = None
        self._audio_hash: str | None = audio_hash
        self._chroma_cqt: np.ndarray | None = None
        self._onset_env: np.ndarray | None = None  # NEW: onset strength envelope
        self._tempogram: np.ndarray | None = None  # NEW: tempogram cache
        self._centroid: np.ndarray | None = None  # NEW: spectral centroid cache

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
                self._stft = librosa.stft(self.y, n_fft=2048, hop_length=512)
        else:
            logging.debug(f"    [CSE Cache Hit] stft 再利用 (source: {self.source})")
        return self._stft

    @property
    def spectro(self) -> np.ndarray:
        if self._spectro is None:
            self._spectro = np.abs(self.stft).astype(np.float32, copy=False)
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
                    bpm, beats = librosa.beat.beat_track(y=self.y, sr=self.sr)
                bpm_val = float(bpm[0] if isinstance(bpm, np.ndarray) else bpm)
                import math

                if math.isnan(bpm_val) or math.isinf(bpm_val):
                    bpm_val = 0.0
            except Exception as e:
                logging.warning(f"beat_track 計算失敗 (source: {self.source}): {e}")
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
        """Spectral Centroidのキャッシュプロパティですわ。"""
        if self._centroid is None:
            logging.debug(
                f"    [CSE Cache Miss] centroid 計算開始 (source: {self.source})"
            )
            with LIBROSA_LOCK:
                raw_centroid = librosa.feature.spectral_centroid(
                    S=self.spectro, sr=self.sr
                )[0]
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
    def hnr(self) -> float:
        if self._hnr is None:
            logging.debug(f"    [CSE Cache Miss] hnr 計算開始 (source: {self.source})")
            self._hnr = _calc_hnr_nap(self)
        else:
            logging.debug(f"    [CSE Cache Hit] hnr 再利用 (source: {self.source})")
        return self._hnr

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


# ─────────────────────────────────────────────
# データクラス群
# ─────────────────────────────────────────────
@dataclass
class TonnetzFeatures:
    """Tonnetz和声特徴量を保持するデータクラスですわ。

    Attributes:
        mean:       時間平均値 (6要素, 各軸の重心)
        std:        時間標準偏差 (6要素, 各軸の散らばり)
        delta_mean: 時間微分の平均 (6要素, 和声変化速度ベクトル)
        seq:        固定長時系列 192要素 = 32フレーム × 6軸, frame-major flatten
                    [f0_ax0, f0_ax1,...,f0_ax5, f1_ax0, ...]
    """

    mean: list[float]
    std: list[float]
    delta_mean: list[float]
    seq: list[float]


@dataclass
class SectionFeatures:
    """セクション構造特徴量。librosa onset_env ベースの境界検出で算出しますの。

    Attributes:
        section_count:      検出されたセクション数
        section_length_std: セクション長の標準偏差 (秒)
        drop_position:      onset_env 最大ピーク位置 ∈ [0, 1] (ドロップ推定位置)
    """

    section_count: int = 0
    section_length_std: float = 0.0
    drop_position: float = 0.0


@dataclass
class GrooveFeatures:
    """Groove / Syncopation 指標。ビートグリッドとonset位置から算出しますの。

    Attributes:
        swing_ratio:       偶数/奇数ビート間隔比 SR = d1/d2 (1.0=ストレート, 2.0=トリプレット)
        syncopation_index: onsetのbeat gridからの平均ズレ量 ∈ [0, 0.5]
        groove_class:      3段階分類 "straight" / "swing" / "heavy_swing"

    参照: Witek et al. 2014 (PLoS ONE) / Longuet-Higgins & Lee metric weight model
    """

    swing_ratio: float = 1.0
    syncopation_index: float = 0.0
    groove_class: str = "straight"


@dataclass
class TemporalSeqFeatures:
    """固定フレーム (FIXED_SEQ_FRAMES=32) 時系列特徴量群。

    seqフィールドはPostgreSQL JSOBのみに格納。
    サマリー統計 (mean/std) はFLACタグにも書き込む。

    seqの補間方式: np.linspace + np.interp"""

    centroid_mean: float
    centroid_std: float
    centroid_seq: list[float]
    rms_seq: list[float]
    chroma_entropy_mean: float
    chroma_entropy_std: float
    chroma_entropy_seq: list[float]
    centroid_delta_mean: float
    centroid_delta_std: float
    centroid_delta_seq: list[float]
    dynamics_range_seq: list[float]


@dataclass
class MfccFeatures:
    """MFCC詳細特徴量群ですわ。"""

    mean: list[float]
    std: list[float]
    entropy: list[float]
    seq: list[list[float]]


@dataclass
class SpectralCentroidFeatures:
    """Spectral Centroid詳細特徴量群ですわ。"""

    mean: float
    std: float
    entropy: float
    seq: list[float]
    peak: float


@dataclass
class RmsFeatures:
    """RMS詳細特徴量群ですわ。"""

    mean: float
    std: float
    entropy: float
    seq: list[float]
    peak: float


@dataclass
class SpectralRolloffFeatures:
    """Spectral Rolloff詳細特徴量群ですわ。"""

    mean: float = 0.0
    std: float = 0.0
    seq: list[float] = field(default_factory=list)


@dataclass
class ZcrFeatures:
    """Zero Crossing Rate詳細特徴量群ですわ。"""

    mean: float = 0.0
    std: float = 0.0
    seq: list[float] = field(default_factory=list)


@dataclass
class ChromaFeatures:
    """Chroma詳細特徴量群ですわ。"""

    mean: list[float]
    std: list[float]
    entropy: list[float]
    seq: list[list[float]]
    peak: list[float]
    entropy_mean: float
    entropy_std: float
    entropy_entropy: float
    entropy_seq: list[float]


@dataclass
class DemucsFeatures:
    """各分離ステム（vocals, drums, bass, other, guitar, piano）の詳細特徴量を包含するクラスですわ！"""

    stems: dict[str, Any] = field(default_factory=dict)
    energy_ratios: dict[str, float] = field(default_factory=dict)

    def to_flac_tags(self) -> dict[str, str]:
        tags = {}
        # 各ステムのエネルギー比率を書き込みますわ
        for name, ratio in self.energy_ratios.items():
            tags[f"DEMUCS_{name.upper()}_ENERGY_RATIO"] = str(int(ratio * 1000))
        # 各ステムの特徴量タグにDEMUCS_プレフィックスを付けてマージしますの
        for name, feat in self.stems.items():
            if hasattr(feat, "to_flac_tags"):
                tags.update(feat.to_flac_tags(prefix=f"DEMUCS_{name.upper()}"))
        return tags

    def to_postgres_dict(self) -> dict[str, Any]:
        res = {}
        for name, feat in self.stems.items():
            if hasattr(feat, "to_postgres_dict"):
                dict_feat = feat.to_postgres_dict(track_id=name)
                dict_feat["scalars"]["energy_ratio"] = self.energy_ratios.get(name, 0.0)
                res[name] = {
                    "scalars": dict_feat["scalars"],
                    "sequences": dict_feat["sequences"],
                }
        return res


@dataclass
class KeyFeatures:
    """Key / Scale 推定結果。

    Attributes:
        key:               推定主音名 (C, C#, D...)
        scale:             推定旋法 (major, minor)
        key_strength:      グローバルキー推定の強度 (最大相関係数)
        key_strength_mean: 時系列キー強度の平均値
        key_strength_std:  時系列キー強度の標準偏差
        key_strength_seq:  時系列キー強度の軌跡 (32要素)
    """

    key: str = "Unknown"
    scale: str = "Unknown"
    key_strength: float = 0.0
    key_strength_mean: float = 0.0
    key_strength_std: float = 0.0
    key_strength_seq: list[float] = field(default_factory=list)


@dataclass
class TempogramFeatures:
    """Tempogram統計値。

    Attributes:
        mean:              テンポグラム全体の平均値
        std:               テンポグラム全体の標準偏差
        peak:              時間ごとの最大テンポ強度の時間平均
        entropy:           時間ごとのテンポ分布のシャノンエントロピー時間平均
        tempo_seq:         時間ごとの支配的テンポ（BPM）軌跡 (32要素固定長)
    """

    mean: float = 0.0
    std: float = 0.0
    peak: float = 0.0
    entropy: float = 0.0
    tempo_seq: list[float] = field(default_factory=list)


@dataclass
class OnsetFeatures:
    """Onset Strength 統計および自己相関。

    Attributes:
        mean:     平均オンセット強度
        std:      オンセット強度の標準偏差
        max:      最大オンセット強度
        p25:      25パーセンタイル
        p50:      50パーセンタイル (中央値)
        p75:      75パーセンタイル
        crest:    クレストファクター (max / mean)
        autocorr: 自己相関 of 低次ラグ圧縮値 (16要素, リズムDNA)
        skew:     オンセット強度の歪度 (Skewness)
        kurt:     オンセット強度の尖度 (Kurtosis)
        onset_strength_seq: オンセット強度の時系列軌跡 (32要素)
    """

    mean: float = 0.0
    std: float = 0.0
    max: float = 0.0
    p25: float = 0.0
    p50: float = 0.0
    p75: float = 0.0
    crest: float = 0.0
    autocorr: list[float] = field(default_factory=list)
    skew: float = 0.0
    kurt: float = 0.0
    onset_strength_seq: list[float] = field(default_factory=list)


# ─────────────────────────────────────────────
# RawFeatures (v4: Single Source of Truth)
# ─────────────────────────────────────────────
@dataclass
class RawFeatures:
    """v4: RMS/centroid統計量を7スカラーに統一、ステム別フィルタ対応 of 生特徴量データクラスですわ！"""

    # ── スカラー（集約不可能 or 利便性の高い代表的統計量） ──
    energy: float = 0.0
    bpm: float = 0.0
    crest_factor: float = 0.0
    snr: float | None = None
    hnr: float = 0.0

    # RMS (音量) 関連スカラー統計量 (直下配置)
    rms_mean: float = 0.0
    rms_std: float = 0.0
    rms_peak: float = 0.0
    rms_max: float = 0.0
    rms_min: float = 0.0
    rms_median: float = 0.0
    rms_entropy: float = 0.0

    # Spectral Centroid (音色の明るさ) 関連スカラー統計量 (直下配置)
    centroid_mean: float = 0.0
    centroid_std: float = 0.0
    centroid_peak: float = 0.0
    centroid_max: float = 0.0
    centroid_min: float = 0.0
    centroid_median: float = 0.0
    centroid_entropy: float = 0.0

    # 復元メンバ
    beat_regularity: float | None = None
    dominant_pitch: str = "Unknown"
    onset_feat: OnsetFeatures | None = None
    tempogram_feat: TempogramFeatures | None = None

    # 新規追加メンバ
    rolloff_mean: float = 0.0
    rolloff_std: float = 0.0
    rolloff_seq: list[float] = field(default_factory=list)
    beat_stability: float = 0.0

    # 復元追加メンバ (以前の不整合解消)
    spectral_bandwidth: float = 0.0
    flatness: float = 0.0
    zcr_mean: float = 0.0
    zcr_std: float = 0.0
    zcr_seq: list[float] = field(default_factory=list)
    contrast_bands: list[float] = field(default_factory=list)
    mfccs: list[float] = field(default_factory=list)

    # ── 音楽構造・調性オブジェクト（集約不可能値） ──
    section: SectionFeatures | None = None
    groove: GrooveFeatures | None = None
    key_feat: KeyFeatures | None = None

    # ── 32フレーム固定長 raw 1次元時系列 (sequences) ──
    rms_seq: list[float] = field(default_factory=list)
    centroid_seq: list[float] = field(default_factory=list)
    centroid_delta_seq: list[float] = field(default_factory=list)
    dynamics_range_seq: list[float] = field(default_factory=list)
    tempogram_tempo: list[float] = field(default_factory=list)
    key_strength_seq: list[float] = field(default_factory=list)

    # ── 32フレーム固定長 raw 2次元時系列 (sequences) ──
    tonnetz: list[list[float]] = field(default_factory=list)
    chroma: list[list[float]] = field(default_factory=list)
    mfcc: list[list[float]] = field(default_factory=list)

    # ── 新規追加の raw 時系列 ──
    chord_sequence: list[str] = field(default_factory=list)
    vocal_f0_seq: list[float] | None = None

    def to_postgres_dict(self, track_id: str) -> dict[str, Any]:
        """ステム種別に応じて scalars/sequences をフィルタリングした辞書を返しますわ！"""
        return {
            "source": track_id,
            "scalars": _stem_filter_scalars(self, track_id),
            "sequences": _stem_filter_sequences(self, track_id),
        }

    def to_flac_tags(self, prefix: str = "") -> dict[str, str]:
        p = f"{prefix}_" if prefix else ""
        tags = {
            f"{p}LIBROSA_RMS_MEAN": str(_safe_int(self.rms_mean, 100)),
            f"{p}LIBROSA_RMS_PEAK": str(_safe_int(self.rms_peak, 100)),
            f"{p}LIBROSA_ENERGY": str(_safe_int(self.energy, 100)),
            f"{p}LIBROSA_BPM": str(_safe_int(self.bpm)),
            f"{p}LIBROSA_DOMINANT_PITCH": self.dominant_pitch,
            f"{p}LIBROSA_SPECTRAL_CENTROID_MEAN": str(_safe_int(self.centroid_mean)),
            f"{p}LIBROSA_SPECTRAL_CENTROID_SD": str(_safe_int(self.centroid_std)),
            f"{p}LIBROSA_SPECTRAL_BANDWIDTH": str(_safe_int(self.spectral_bandwidth)),
            f"{p}LIBROSA_FLATNESS": str(_safe_int(self.flatness, 100)),
            f"{p}LIBROSA_ROLLOFF": _safe_float_str(self.rolloff_mean),
            f"{p}LIBROSA_ZCR_MEAN": _safe_float_str(self.zcr_mean),
            f"{p}LIBROSA_ZCR_STD": _safe_float_str(self.zcr_std),
            f"{p}LIBROSA_ZCR": _safe_float_str(self.zcr_mean),
            f"{p}LIBROSA_HNR": _safe_float_str(self.hnr),
            # 新規: Section Structure
            f"{p}LIBROSA_SECTION_COUNT": str(self.section.section_count)
            if self.section
            else "0",
            f"{p}LIBROSA_SECTION_LENGTH_STD": str(
                _safe_int(self.section.section_length_std, 100)
            )
            if self.section
            else "0",
            f"{p}LIBROSA_DROP_POSITION": str(
                _safe_int(self.section.drop_position, 1000)
            )
            if self.section
            else "0",
            # 新規: Groove
            f"{p}LIBROSA_SWING_RATIO": str(_safe_int(self.groove.swing_ratio, 100))
            if self.groove
            else "100",
            f"{p}LIBROSA_SYNCOPATION_INDEX": str(
                _safe_int(self.groove.syncopation_index, 1000)
            )
            if self.groove
            else "0",
            f"{p}LIBROSA_GROOVE_CLASS": self.groove.groove_class
            if self.groove
            else "straight",
            # 新規: Dynamics
            f"{p}LIBROSA_CREST_FACTOR": str(_safe_int(self.crest_factor, 100)),
        }
        if self.beat_regularity is not None:
            tags[f"{p}LIBROSA_BEAT_REGULARITY"] = str(
                _safe_int(self.beat_regularity, 100)
            )
        if self.beat_stability is not None:
            tags[f"{p}LIBROSA_BEAT_STABILITY"] = str(
                _safe_int(self.beat_stability, 1000)
            )
        if self.snr is not None:
            tags[f"{p}LIBROSA_SNR"] = _safe_float_str(self.snr)
        for bi, val in enumerate(self.contrast_bands):
            tags[f"{p}LIBROSA_CONTRAST_B{bi}"] = str(_safe_int(val, 100))
        for ci, val in enumerate(self.mfccs):
            tags[f"{p}LIBROSA_MFCC{ci:02d}"] = str(_safe_int(val, 100))

        # 新規: Key / Scale
        if self.key_feat is not None:
            kf = self.key_feat
            tags[f"{p}LIBROSA_KEY"] = kf.key
            tags[f"{p}LIBROSA_SCALE"] = kf.scale
            tags[f"{p}LIBROSA_KEY_STRENGTH"] = str(_safe_int(kf.key_strength, 1000))
            tags[f"{p}LIBROSA_KEY_STRENGTH_MEAN"] = str(
                _safe_int(kf.key_strength_mean, 1000)
            )
            tags[f"{p}LIBROSA_KEY_STRENGTH_STD"] = str(
                _safe_int(kf.key_strength_std, 1000)
            )
        # 新規: Tempogram
        if self.tempogram_feat is not None:
            tf = self.tempogram_feat
            tags[f"{p}LIBROSA_TEMPOGRAM_MEAN"] = str(_safe_int(tf.mean, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_STD"] = str(_safe_int(tf.std, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_PEAK"] = str(_safe_int(tf.peak, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_ENTROPY"] = str(_safe_int(tf.entropy, 1000))
        # 新規: Onset
        if self.onset_feat is not None:
            of = self.onset_feat
            tags[f"{p}LIBROSA_ONSET_MEAN"] = str(_safe_int(of.mean, 1000))
            tags[f"{p}LIBROSA_ONSET_STD"] = str(_safe_int(of.std, 1000))
            tags[f"{p}LIBROSA_ONSET_MAX"] = str(_safe_int(of.max, 1000))
            tags[f"{p}LIBROSA_ONSET_P25"] = str(_safe_int(of.p25, 1000))
            tags[f"{p}LIBROSA_ONSET_P50"] = str(_safe_int(of.p50, 1000))
            tags[f"{p}LIBROSA_ONSET_P75"] = str(_safe_int(of.p75, 1000))
            tags[f"{p}LIBROSA_ONSET_CREST"] = str(_safe_int(of.crest, 1000))
            tags[f"{p}LIBROSA_ONSET_SKEW"] = str(_safe_int(of.skew, 1000))
            tags[f"{p}LIBROSA_ONSET_KURT"] = str(_safe_int(of.kurt, 1000))

        # 新規詳細特徴量の代表統計をFLACタグに反映
        tags[f"{p}LIBROSA_RMS_STD"] = str(_safe_int(self.rms_std, 100))
        tags[f"{p}LIBROSA_RMS_ENTROPY"] = str(_safe_int(self.rms_entropy, 1000))
        tags[f"{p}LIBROSA_SPECTRAL_CENTROID_ENTROPY"] = str(
            _safe_int(self.centroid_entropy, 1000)
        )
        tags[f"{p}LIBROSA_SPECTRAL_CENTROID_PEAK"] = str(_safe_int(self.centroid_peak))
        tags[f"{p}LIBROSA_ROLLOFF_MEAN"] = str(_safe_int(self.rolloff_mean))
        tags[f"{p}LIBROSA_ROLLOFF_STD"] = str(_safe_int(self.rolloff_std))

        return tags


def _calc_rms_stats(rms: np.ndarray) -> dict[str, float]:
    """RMS時系列から7スカラー統計量を算出しますわ！"""
    mean_val = float(np.mean(rms))
    std_val = float(np.std(rms))
    peak_val = float(np.max(rms))
    max_val = peak_val
    min_val = float(np.min(rms))
    median_val = float(np.median(rms))
    abs_rms = np.abs(rms)
    s = np.sum(abs_rms)
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_rms / s
        entropy_val = float(-np.sum(p * np.log2(p + 1e-10)))
    return {
        "mean": mean_val,
        "std": std_val,
        "peak": peak_val,
        "max": max_val,
        "min": min_val,
        "median": median_val,
        "entropy": entropy_val,
    }


def _calc_centroid_stats(centroid: np.ndarray) -> dict[str, float]:
    """Spectral Centroid時系列から7スカラー統計量を算出しますわ！"""
    mean_val = float(np.mean(centroid))
    std_val = float(np.std(centroid))
    peak_val = float(np.max(centroid))
    max_val = peak_val
    min_val = float(np.min(centroid))
    median_val = float(np.median(centroid))
    abs_cent = np.abs(centroid)
    s = np.sum(abs_cent)
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_cent / s
        entropy_val = float(-np.sum(p * np.log2(p + 1e-10)))
    return {
        "mean": mean_val,
        "std": std_val,
        "peak": peak_val,
        "max": max_val,
        "min": min_val,
        "median": median_val,
        "entropy": entropy_val,
    }


def _calc_chord_sequence(ctx: AudioContext) -> list[str]:
    """12Dクロマと24コードのピアソン相関から32フレームのコード名時系列を生成しますわ！"""
    if ctx.source not in ("mix", "bass", "vocal", "piano", "guitar", "other"):
        return ["C" for _ in range(FIXED_SEQ_FRAMES)]

    try:
        chroma_cqt = ctx.chroma_cqt  # (12, T)
        T_len = chroma_cqt.shape[1]
        if T_len == 0:
            return ["C" for _ in range(FIXED_SEQ_FRAMES)]

        chords_dic: dict[str, list[str]] = cast(
            dict[str, list[str]], CHORDS_DIC["chords_dic"]
        )
        chord_names: list[str] = sorted(chords_dic.keys())
        chord_vectors: list[np.ndarray] = []
        for cn in chord_names:
            notes_in_chord = chords_dic[cn]
            vec = np.zeros(12)
            for n in notes_in_chord:
                idx = NOTES.index(n)
                vec[idx] = 1.0
            chord_vectors.append(vec)

        chroma_norm = chroma_cqt / (
            np.max(np.abs(chroma_cqt), axis=0, keepdims=True) + 1e-10
        )
        frame_chords: list[str] = []
        for t in range(T_len):
            frame_vec = cast(np.ndarray, chroma_norm[:, t])
            best_corr = -2.0
            best_chord = "C"
            for cv, cn in zip(chord_vectors, chord_names):
                corr = float(
                    np.dot(frame_vec, cv)
                    / (np.linalg.norm(frame_vec) * np.linalg.norm(cv) + 1e-10)
                )
                if corr > best_corr:
                    best_corr = corr
                    best_chord = cn
            frame_chords.append(best_chord)

        chord_seq = _resample_to_fixed_frames(
            np.array([chord_names.index(c) for c in frame_chords])
        )
        result: list[str] = []
        for idx in chord_seq:  # type: ignore[var-annotated]
            idx_int = int(round(float(idx))) % len(chord_names)  # type: ignore[assignment]
            result.append(chord_names[idx_int])
        return result[:FIXED_SEQ_FRAMES]

    except Exception as e:
        logging.warning(
            f"[ChordSequence] コード列推定エラー (source: {ctx.source}): {e}"
        )
        return ["C" for _ in range(FIXED_SEQ_FRAMES)]


def _calc_vocal_f0_seq(ctx: AudioContext) -> list[float] | None:
    """vocalsステムに対してYINピッチ検出を行い、32フレームのピッチ時系列(Hz)を生成しますわ！"""
    if ctx.source != "vocals":
        return None

    try:
        with LIBROSA_LOCK:
            f0 = librosa.yin(
                ctx.y,
                sr=ctx.sr,
                fmin=float(librosa.note_to_hz("C2")),
                fmax=float(librosa.note_to_hz("C7")),
            )
        valid_f0 = f0[f0 > 0.0]
        if len(valid_f0) == 0:
            return [0.0] * FIXED_SEQ_FRAMES
        seq = _resample_to_fixed_frames(f0)
        return seq
    except Exception as e:
        logging.warning(f"[VocalF0] ピッチ検出エラー (source: {ctx.source}): {e}")
        return [0.0] * FIXED_SEQ_FRAMES


# ─────────────────────────────────────────────
# EssentiaFeatures
# ─────────────────────────────────────────────
class EssentiaFeatures:
    """ONNX(Essentia)による分類結果の生 float (確率) を保持するクラスですわ！"""

    def __init__(self, predictions: dict[str, float]):
        self.predictions = predictions

    def to_flac_tags(self) -> dict[str, str]:
        return {k: str(_safe_int(v, 1000)) for k, v in self.predictions.items()}

    def to_postgres_dict(self) -> dict[str, Any]:
        try:
            import math

            return {
                k.lower(): (
                    None if math.isnan(float(v)) or math.isinf(float(v)) else float(v)
                )
                for k, v in self.predictions.items()
            }
        except (TypeError, ValueError):
            return {k.lower(): 0.0 for k, v in self.predictions.items()}


def _safe_int(val: Any, multiplier: float = 1.0, default: int = 0) -> int:
    if val is None:
        return default
    try:
        import math

        f_val = float(val)
        if math.isnan(f_val) or math.isinf(f_val):
            return default
        return int(f_val * multiplier)
    except (TypeError, ValueError, OverflowError):
        return default


def _safe_float_str(val: Any, default: str = "0.0") -> str:
    if val is None:
        return default
    try:
        import math

        f_val = float(val)
        if math.isnan(f_val) or math.isinf(f_val):
            return default
        return str(f_val)
    except (TypeError, ValueError):
        return default


# ─────────────────────────────────────────────
# LibrosaFeatures
# ─────────────────────────────────────────────
class LibrosaFeatures:
    """Librosaによる生特徴量を保持し、FLACタグとPostgres辞書の双方を出力するクラスですわ！"""

    def __init__(
        self,
        rms_mean: float,
        rms_peak: float,
        energy: float,
        bpm: float,
        beat_regularity: float | None,
        dominant_pitch: str,
        spectral_centroid_mean: float,
        spectral_centroid_sd: float,
        spectral_bandwidth: float,
        flatness: float,
        rolloff: float,
        contrast_bands: list[float],
        zcr: float,
        snr: float | None,
        hnr: float,
        mfccs: list[float],
        tonnetz: TonnetzFeatures | None = None,
        # ── 新規追加フィールド ──────────────────────
        section: SectionFeatures | None = None,
        groove: GrooveFeatures | None = None,
        crest_factor: float = 0.0,
        temporal_seq: TemporalSeqFeatures | None = None,
        key_feat: KeyFeatures | None = None,
        tempogram_feat: TempogramFeatures | None = None,
        onset_feat: OnsetFeatures | None = None,
        # ── 追加した詳細特徴量オブジェクト ───────────
        rms_obj: RmsFeatures | None = None,
        centroid_obj: SpectralCentroidFeatures | None = None,
        mfcc_obj: MfccFeatures | None = None,
        chroma_obj: ChromaFeatures | None = None,
    ):
        self.rms_mean = rms_mean
        self.rms_peak = rms_peak
        self.energy = energy
        self.bpm = bpm
        self.beat_regularity = beat_regularity
        self.dominant_pitch = dominant_pitch
        self.spectral_centroid_mean = spectral_centroid_mean
        self.spectral_centroid_sd = spectral_centroid_sd
        self.spectral_bandwidth = spectral_bandwidth
        self.flatness = flatness
        self.rolloff = rolloff
        self.contrast_bands = contrast_bands
        self.zcr = zcr
        self.snr = snr
        self.hnr = hnr
        self.mfccs = mfccs
        self.tonnetz = tonnetz
        # 新規
        self.section = section if section is not None else SectionFeatures()
        self.groove = groove if groove is not None else GrooveFeatures()
        self.crest_factor = crest_factor
        self.temporal_seq = temporal_seq
        self.key_feat = key_feat if key_feat is not None else KeyFeatures()
        self.tempogram_feat = (
            tempogram_feat if tempogram_feat is not None else TempogramFeatures()
        )
        self.onset_feat = onset_feat if onset_feat is not None else OnsetFeatures()
        # 詳細オブジェクト
        self.rms_obj = rms_obj
        self.centroid_obj = centroid_obj
        self.mfcc_obj = mfcc_obj
        self.chroma_obj = chroma_obj

    def to_flac_tags(self, prefix: str = "") -> dict[str, str]:
        p = f"{prefix}_" if prefix else ""
        tags = {
            f"{p}LIBROSA_RMS_MEAN": str(_safe_int(self.rms_mean, 100)),
            f"{p}LIBROSA_RMS_PEAK": str(_safe_int(self.rms_peak, 100)),
            f"{p}LIBROSA_ENERGY": str(_safe_int(self.energy, 100)),
            f"{p}LIBROSA_BPM": str(_safe_int(self.bpm)),
            f"{p}LIBROSA_DOMINANT_PITCH": self.dominant_pitch,
            f"{p}LIBROSA_SPECTRAL_CENTROID_MEAN": str(
                _safe_int(self.spectral_centroid_mean)
            ),
            f"{p}LIBROSA_SPECTRAL_CENTROID_SD": str(
                _safe_int(self.spectral_centroid_sd)
            ),
            f"{p}LIBROSA_SPECTRAL_BANDWIDTH": str(_safe_int(self.spectral_bandwidth)),
            f"{p}LIBROSA_FLATNESS": str(_safe_int(self.flatness, 100)),
            f"{p}LIBROSA_ROLLOFF": _safe_float_str(self.rolloff),
            f"{p}LIBROSA_ZCR": _safe_float_str(self.zcr),
            f"{p}LIBROSA_HNR": _safe_float_str(self.hnr),
            # 新規: Section Structure
            f"{p}LIBROSA_SECTION_COUNT": str(self.section.section_count),
            f"{p}LIBROSA_SECTION_LENGTH_STD": str(
                _safe_int(self.section.section_length_std, 100)
            ),
            f"{p}LIBROSA_DROP_POSITION": str(
                _safe_int(self.section.drop_position, 1000)
            ),
            # 新規: Groove
            f"{p}LIBROSA_SWING_RATIO": str(_safe_int(self.groove.swing_ratio, 100)),
            f"{p}LIBROSA_SYNCOPATION_INDEX": str(
                _safe_int(self.groove.syncopation_index, 1000)
            ),
            f"{p}LIBROSA_GROOVE_CLASS": self.groove.groove_class,
            # 新規: Dynamics
            f"{p}LIBROSA_CREST_FACTOR": str(_safe_int(self.crest_factor, 100)),
        }
        if self.beat_regularity is not None:
            tags[f"{p}LIBROSA_BEAT_REGULARITY"] = str(
                _safe_int(self.beat_regularity, 100)
            )
        if self.snr is not None:
            tags[f"{p}LIBROSA_SNR"] = _safe_float_str(self.snr)
        for bi, val in enumerate(self.contrast_bands):
            tags[f"{p}LIBROSA_CONTRAST_B{bi}"] = str(_safe_int(val, 100))
        for ci, val in enumerate(self.mfccs):
            tags[f"{p}LIBROSA_MFCC{ci:02d}"] = str(_safe_int(val, 100))
        # 新規: TemporalSeqFeatures サマリー統計のみFLACタグへ
        if self.temporal_seq is not None:
            ts = self.temporal_seq
            tags[f"{p}LIBROSA_CENTROID_SEQ_MEAN"] = str(_safe_int(ts.centroid_mean))
            tags[f"{p}LIBROSA_CENTROID_SEQ_STD"] = str(_safe_int(ts.centroid_std))
            tags[f"{p}LIBROSA_CHROMA_ENTROPY_MEAN"] = str(
                _safe_int(ts.chroma_entropy_mean, 1000)
            )
            tags[f"{p}LIBROSA_CHROMA_ENTROPY_STD"] = str(
                _safe_int(ts.chroma_entropy_std, 1000)
            )
            tags[f"{p}LIBROSA_CENTROID_DELTA_MEAN"] = str(
                _safe_int(ts.centroid_delta_mean)
            )
            tags[f"{p}LIBROSA_CENTROID_DELTA_STD"] = str(
                _safe_int(ts.centroid_delta_std)
            )
        # 新規: Key / Scale
        if self.key_feat is not None:
            kf = self.key_feat
            tags[f"{p}LIBROSA_KEY"] = kf.key
            tags[f"{p}LIBROSA_SCALE"] = kf.scale
            tags[f"{p}LIBROSA_KEY_STRENGTH"] = str(_safe_int(kf.key_strength, 1000))
            tags[f"{p}LIBROSA_KEY_STRENGTH_MEAN"] = str(
                _safe_int(kf.key_strength_mean, 1000)
            )
            tags[f"{p}LIBROSA_KEY_STRENGTH_STD"] = str(
                _safe_int(kf.key_strength_std, 1000)
            )
        # 新規: Tempogram
        if self.tempogram_feat is not None:
            tf = self.tempogram_feat
            tags[f"{p}LIBROSA_TEMPOGRAM_MEAN"] = str(_safe_int(tf.mean, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_STD"] = str(_safe_int(tf.std, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_PEAK"] = str(_safe_int(tf.peak, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_ENTROPY"] = str(_safe_int(tf.entropy, 1000))
        # 新規: Onset
        if self.onset_feat is not None:
            of = self.onset_feat
            tags[f"{p}LIBROSA_ONSET_MEAN"] = str(_safe_int(of.mean, 1000))
            tags[f"{p}LIBROSA_ONSET_STD"] = str(_safe_int(of.std, 1000))
            tags[f"{p}LIBROSA_ONSET_MAX"] = str(_safe_int(of.max, 1000))
            tags[f"{p}LIBROSA_ONSET_P25"] = str(_safe_int(of.p25, 1000))
            tags[f"{p}LIBROSA_ONSET_P50"] = str(_safe_int(of.p50, 1000))
            tags[f"{p}LIBROSA_ONSET_P75"] = str(_safe_int(of.p75, 1000))
            tags[f"{p}LIBROSA_ONSET_CREST"] = str(_safe_int(of.crest, 1000))

        # 新規詳細特徴量の代表統計をFLACタグに反映
        if self.rms_obj is not None:
            tags[f"{p}LIBROSA_RMS_STD"] = str(_safe_int(self.rms_obj.std, 100))
            tags[f"{p}LIBROSA_RMS_ENTROPY"] = str(_safe_int(self.rms_obj.entropy, 1000))
        if self.centroid_obj is not None:
            tags[f"{p}LIBROSA_SPECTRAL_CENTROID_ENTROPY"] = str(
                _safe_int(self.centroid_obj.entropy, 1000)
            )
            tags[f"{p}LIBROSA_SPECTRAL_CENTROID_PEAK"] = str(
                _safe_int(self.centroid_obj.peak)
            )
        if self.mfcc_obj is not None:
            for i in range(len(self.mfcc_obj.mean)):
                tags[f"{p}LIBROSA_MFCC_MEAN_{i:02d}"] = str(
                    _safe_int(self.mfcc_obj.mean[i], 100)
                )
                tags[f"{p}LIBROSA_MFCC_STD_{i:02d}"] = str(
                    _safe_int(self.mfcc_obj.std[i], 100)
                )
                tags[f"{p}LIBROSA_MFCC_ENTROPY_{i:02d}"] = str(
                    _safe_int(self.mfcc_obj.entropy[i], 1000)
                )
        if self.chroma_obj is not None:
            for i in range(len(self.chroma_obj.mean)):
                tags[f"{p}LIBROSA_CHROMA_MEAN_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.mean[i], 1000)
                )
                tags[f"{p}LIBROSA_CHROMA_STD_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.std[i], 1000)
                )
                tags[f"{p}LIBROSA_CHROMA_ENTROPY_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.entropy[i], 1000)
                )
                tags[f"{p}LIBROSA_CHROMA_PEAK_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.peak[i], 1000)
                )
            tags[f"{p}LIBROSA_CHROMA_ENTROPY_ENTROPY"] = str(
                _safe_int(self.chroma_obj.entropy_entropy, 1000)
            )

        return tags

    def to_postgres_dict(self, track_id: str = "mix") -> dict[str, Any]:
        """PostgreSQL JSONB挿入用: scalars と sequences に直交分離した辞書を返しますわ！"""
        scalars: dict[str, Any] = {
            "rms_mean": float(self.rms_mean),
            "rms_peak": float(self.rms_peak),
            "energy": float(self.energy),
            "bpm": float(self.bpm),
            "beat_regularity": float(self.beat_regularity)
            if self.beat_regularity is not None
            else None,
            "dominant_pitch": self.dominant_pitch,
            "spectral_centroid_mean": float(self.spectral_centroid_mean),
            "spectral_centroid_sd": float(self.spectral_centroid_sd),
            "spectral_bandwidth": float(self.spectral_bandwidth),
            "flatness": float(self.flatness),
            "rolloff": float(self.rolloff),
            "zcr": float(self.zcr),
            "snr": float(self.snr) if self.snr is not None else None,
            "hnr": float(self.hnr),
            "contrast": [float(v) for v in self.contrast_bands],
            "mfcc": [float(v) for v in self.mfccs],
            # Section Structure
            "section_count": self.section.section_count,
            "section_length_std": float(self.section.section_length_std),
            "drop_position": float(self.section.drop_position),
            # Groove
            "swing_ratio": float(self.groove.swing_ratio),
            "syncopation_index": float(self.groove.syncopation_index),
            "groove_class": self.groove.groove_class,
            # Dynamics
            "crest_factor": float(self.crest_factor),
        }

        sequences: dict[str, Any] = {}

        if self.tonnetz is not None:
            scalars["tonnetz_mean"] = self.tonnetz.mean
            scalars["tonnetz_std"] = self.tonnetz.std
            scalars["tonnetz_delta_mean"] = self.tonnetz.delta_mean
            sequences["tonnetz"] = self.tonnetz.seq

        # TemporalSeqFeatures
        if self.temporal_seq is not None:
            ts = self.temporal_seq
            scalars["centroid_seq_mean"] = float(ts.centroid_mean)
            scalars["centroid_seq_std"] = float(ts.centroid_std)
            scalars["chroma_entropy_mean"] = float(ts.chroma_entropy_mean)
            scalars["chroma_entropy_std"] = float(ts.chroma_entropy_std)
            scalars["centroid_delta_mean"] = float(ts.centroid_delta_mean)
            scalars["centroid_delta_std"] = float(ts.centroid_delta_std)

            # sequences に移動
            sequences["centroid"] = ts.centroid_seq  # 32要素
            sequences["rms"] = ts.rms_seq  # 32要素
            sequences["chroma_entropy"] = ts.chroma_entropy_seq  # 32要素
            sequences["centroid_delta"] = ts.centroid_delta_seq  # 32要素
            sequences["dynamics_range"] = ts.dynamics_range_seq  # 32要素 (NEW!)

        # Key / Scale
        if self.key_feat is not None:
            kf = self.key_feat
            scalars["key"] = kf.key
            scalars["scale"] = kf.scale
            scalars["key_strength"] = float(kf.key_strength)
            scalars["key_strength_mean"] = float(kf.key_strength_mean)
            scalars["key_strength_std"] = float(kf.key_strength_std)
            sequences["key_strength"] = kf.key_strength_seq  # 32要素

        # Tempogram
        if self.tempogram_feat is not None:
            tf = self.tempogram_feat
            scalars["tempogram_mean"] = float(tf.mean)
            scalars["tempogram_std"] = float(tf.std)
            scalars["tempogram_peak"] = float(tf.peak)
            scalars["tempogram_entropy"] = float(tf.entropy)
            sequences["tempogram_tempo"] = tf.tempo_seq  # 32要素 (TempoSeq)

        # Onset
        if self.onset_feat is not None:
            of = self.onset_feat
            scalars["onset_mean"] = float(of.mean)
            scalars["onset_std"] = float(of.std)
            scalars["onset_max"] = float(of.max)
            scalars["onset_p25"] = float(of.p25)
            scalars["onset_p50"] = float(of.p50)
            scalars["onset_p75"] = float(of.p75)
            scalars["onset_crest"] = float(of.crest)
            sequences["onset_autocorr"] = of.autocorr  # 16要素

        # 詳細オブジェクトの整理
        if self.rms_obj is not None:
            scalars["rms"] = {
                "mean": float(self.rms_obj.mean),
                "std": float(self.rms_obj.std),
                "entropy": float(self.rms_obj.entropy),
                "peak": float(self.rms_obj.peak),
            }
            sequences["rms_detail"] = self.rms_obj.seq
        if self.centroid_obj is not None:
            scalars["spectral_centroid"] = {
                "mean": float(self.centroid_obj.mean),
                "std": float(self.centroid_obj.std),
                "entropy": float(self.centroid_obj.entropy),
                "peak": float(self.centroid_obj.peak),
            }
            sequences["spectral_centroid_detail"] = self.centroid_obj.seq
        if self.mfcc_obj is not None:
            scalars["mfcc_detail"] = {
                "mean": [float(v) for v in self.mfcc_obj.mean],
                "std": [float(v) for v in self.mfcc_obj.std],
                "entropy": [float(v) for v in self.mfcc_obj.entropy],
            }
            sequences["mfcc_detail"] = [
                [float(x) for x in dim_seq] for dim_seq in self.mfcc_obj.seq
            ]
        if self.chroma_obj is not None:
            scalars["chroma"] = {
                "mean": [float(v) for v in self.chroma_obj.mean],
                "std": [float(v) for v in self.chroma_obj.std],
                "entropy": [float(v) for v in self.chroma_obj.entropy],
                "peak": [float(v) for v in self.chroma_obj.peak],
                "entropy_mean": float(self.chroma_obj.entropy_mean),
                "entropy_std": float(self.chroma_obj.entropy_std),
                "entropy_entropy": float(self.chroma_obj.entropy_entropy),
            }
            sequences["chroma_detail"] = [
                [float(x) for x in dim_seq] for dim_seq in self.chroma_obj.seq
            ]
            sequences["chroma_entropy_detail"] = self.chroma_obj.entropy_seq

        return {"source": track_id, "scalars": scalars, "sequences": sequences}


# ─────────────────────────────────────────────
# RawFeatures.to_postgres_dict (v4 ステム別フィルタリング)
# ─────────────────────────────────────────────
def _stem_filter_scalars(raw: RawFeatures, track_id: str) -> dict[str, Any]:
    """全ステム共通ですべてのスカラー特徴量を返却しますわ！"""
    scalars: dict[str, Any] = {
        "energy": float(raw.energy),
        "bpm": float(raw.bpm),
        "crest_factor": float(raw.crest_factor),
        "snr": float(raw.snr) if raw.snr is not None else None,
        "hnr": float(raw.hnr),
        # RMS 7スカラー
        "rms_mean": float(raw.rms_mean),
        "rms_std": float(raw.rms_std),
        "rms_peak": float(raw.rms_peak),
        "rms_max": float(raw.rms_max),
        "rms_min": float(raw.rms_min),
        "rms_median": float(raw.rms_median),
        "rms_entropy": float(raw.rms_entropy),
        # Centroid 7スカラー
        "centroid_mean": float(raw.centroid_mean),
        "centroid_std": float(raw.centroid_std),
        "centroid_peak": float(raw.centroid_peak),
        "centroid_max": float(raw.centroid_max),
        "centroid_min": float(raw.centroid_min),
        "centroid_median": float(raw.centroid_median),
        "centroid_entropy": float(raw.centroid_entropy),
        # Spectral Rolloff スカラー
        "rolloff_mean": float(raw.rolloff_mean),
        "rolloff_std": float(raw.rolloff_std),
        # 復元追加スカラー (以前の不整合解消)
        "spectral_bandwidth": float(raw.spectral_bandwidth),
        "flatness": float(raw.flatness),
        "zcr_mean": float(raw.zcr_mean),
        "zcr_std": float(raw.zcr_std),
        "zcr": float(raw.zcr_mean),
        "contrast": [float(v) for v in raw.contrast_bands],
        "mfcc": [float(v) for v in raw.mfccs],
        # ビート/ピッチ
        "beat_regularity": float(raw.beat_regularity)
        if raw.beat_regularity is not None
        else None,
        "beat_stability": float(raw.beat_stability),
        "dominant_pitch": raw.dominant_pitch,
    }

    # 全ステム共通で構造/調性/リズム情報を追加しますの！
    if raw.tempogram_feat is not None:
        scalars["tempogram"] = {
            "mean": float(raw.tempogram_feat.mean),
            "std": float(raw.tempogram_feat.std),
            "peak": float(raw.tempogram_feat.peak),
            "entropy": float(raw.tempogram_feat.entropy),
        }
    if raw.section is not None:
        scalars["section"] = {
            "section_count": raw.section.section_count,
            "section_length_std": float(raw.section.section_length_std),
            "drop_position": float(raw.section.drop_position),
        }
    if raw.groove is not None:
        scalars["groove"] = {
            "swing_ratio": float(raw.groove.swing_ratio),
            "syncopation_index": float(raw.groove.syncopation_index),
            "groove_class": raw.groove.groove_class,
        }
    if raw.key_feat is not None:
        scalars["key"] = raw.key_feat.key
        scalars["scale"] = raw.key_feat.scale
        scalars["key_strength"] = float(raw.key_feat.key_strength)
    if raw.onset_feat is not None:
        scalars["onset"] = {
            "mean": float(raw.onset_feat.mean),
            "std": float(raw.onset_feat.std),
            "max": float(raw.onset_feat.max),
            "p25": float(raw.onset_feat.p25),
            "p50": float(raw.onset_feat.p50),
            "p75": float(raw.onset_feat.p75),
            "crest": float(raw.onset_feat.crest),
            "skew": float(raw.onset_feat.skew),
            "kurt": float(raw.onset_feat.kurt),
        }
    return scalars


def _stem_filter_sequences(raw: RawFeatures, track_id: str) -> dict[str, Any]:
    """全ステム共通ですべてのシーケンス特徴量を返却しますわ！"""
    sequences: dict[str, Any] = {}

    if raw.rms_seq:
        sequences["rms"] = raw.rms_seq
    if raw.centroid_seq:
        sequences["centroid"] = raw.centroid_seq
    if raw.zcr_seq:
        sequences["zcr"] = raw.zcr_seq
    if raw.tempogram_tempo:
        sequences["tempogram_tempo"] = raw.tempogram_tempo
    if raw.centroid_delta_seq:
        sequences["centroid_delta"] = raw.centroid_delta_seq
    if raw.dynamics_range_seq:
        sequences["dynamics_range"] = raw.dynamics_range_seq
    if raw.key_strength_seq:
        sequences["key_strength"] = raw.key_strength_seq
    if raw.tonnetz:
        sequences["tonnetz"] = raw.tonnetz
    if raw.chroma:
        sequences["chroma"] = raw.chroma
    if raw.mfcc:
        sequences["mfcc"] = raw.mfcc
    if raw.chord_sequence:
        sequences["chord_sequence"] = raw.chord_sequence
    if raw.rolloff_seq:
        sequences["rolloff"] = raw.rolloff_seq
    if raw.vocal_f0_seq is not None:
        sequences["vocal_f0_seq"] = raw.vocal_f0_seq
    if raw.onset_feat is not None:
        sequences["onset_strength"] = raw.onset_feat.onset_strength_seq
        sequences["onset_autocorr"] = raw.onset_feat.autocorr

    return sequences


def _calc_time_entropy(seq: np.ndarray | list[float]) -> float:
    """非負の時系列データ seq のシャノンエントロピーを算出しますわ。"""
    abs_seq = np.abs(np.asarray(seq))
    s = np.sum(abs_seq)
    if s < 1e-10:
        p = np.ones_like(abs_seq) / len(abs_seq)
    else:
        p = abs_seq / s
    return float(-np.sum(p * np.log2(p + 1e-10)))


def _calc_rms_features(ctx: AudioContext) -> RmsFeatures:
    """RMS詳細特徴量群を算出しますわ。"""
    rms = librosa.feature.rms(S=ctx.spectro)[0]  # (T,)
    mean = float(np.mean(rms))
    std = float(np.std(rms))
    seq = _resample_to_fixed_frames(rms)
    entropy = _calc_time_entropy(seq)
    peak = float(np.max(rms))
    return RmsFeatures(mean=mean, std=std, entropy=entropy, seq=seq, peak=peak)


def _calc_centroid_features(ctx: AudioContext) -> SpectralCentroidFeatures:
    """Spectral Centroid詳細特徴量群を算出しますわ。"""
    cent = librosa.feature.spectral_centroid(S=ctx.spectro, sr=ctx.sr)[0]  # (T,)
    mean = float(np.mean(cent))
    std = float(np.std(cent))
    seq = _resample_to_fixed_frames(cent)
    entropy = _calc_time_entropy(seq)
    peak = float(np.max(cent))
    return SpectralCentroidFeatures(
        mean=mean, std=std, entropy=entropy, seq=seq, peak=peak
    )


def _calc_mfcc_features(ctx: AudioContext) -> MfccFeatures:
    """MFCC詳細特徴量群を算出しますわ。"""
    log_mel = librosa.power_to_db(ctx.mel, ref=np.max)
    mfcc = librosa.feature.mfcc(S=log_mel, n_mfcc=8)  # (8, T)
    means = np.mean(mfcc, axis=1).tolist()
    stds = np.std(mfcc, axis=1).tolist()

    seqs = []
    entropies = []
    for i in range(8):
        dim_seq = _resample_to_fixed_frames(mfcc[i])
        seqs.append(dim_seq)
        entropies.append(_calc_time_entropy(dim_seq))

    return MfccFeatures(mean=means, std=stds, entropy=entropies, seq=seqs)


def _calc_chroma_features(ctx: AudioContext) -> ChromaFeatures:
    """Chroma詳細特徴量群を算出しますわ。"""
    chroma = ctx.chroma  # (12, T)
    means = np.mean(chroma, axis=1).tolist()
    stds = np.std(chroma, axis=1).tolist()

    seqs = []
    entropies = []
    peaks = []
    for i in range(12):
        dim_seq = _resample_to_fixed_frames(chroma[i])
        seqs.append(dim_seq)
        entropies.append(_calc_time_entropy(dim_seq))
        peaks.append(float(np.max(chroma[i])))

    # 各フレームのシャノンエントロピーの時系列
    p = chroma / (chroma.sum(axis=0, keepdims=True) + 1e-10)
    frame_entropies = -np.sum(p * np.log2(p + 1e-10), axis=0)  # (T,)

    entropy_mean = float(np.mean(frame_entropies))
    entropy_std = float(np.std(frame_entropies))
    entropy_seq = _resample_to_fixed_frames(frame_entropies)
    entropy_entropy = _calc_time_entropy(entropy_seq)

    return ChromaFeatures(
        mean=means,
        std=stds,
        entropy=entropies,
        seq=seqs,
        peak=peaks,
        entropy_mean=entropy_mean,
        entropy_std=entropy_std,
        entropy_entropy=entropy_entropy,
        entropy_seq=entropy_seq,
    )


# ─────────────────────────────────────────────
# 特徴量計算関数群（既存）
# ─────────────────────────────────────────────
def _calc_rms_mean(ctx: AudioContext) -> float:
    rms = librosa.feature.rms(S=ctx.spectro)
    return float(np.mean(rms))


def _calc_rms_peak(ctx: AudioContext) -> float:
    rms = librosa.feature.rms(S=ctx.spectro)
    return float(np.max(rms))


def _calc_energy(ctx: AudioContext) -> float:
    return float(np.sqrt(np.mean(ctx.y**2)))


def _calc_bpm(ctx: AudioContext) -> float:
    return ctx.tempobeat[0]


def _calc_beat_regularity(ctx: AudioContext) -> float | None:
    bpm, beats = ctx.tempobeat
    if len(beats) > 1:
        ibi = np.diff(librosa.frames_to_time(beats, sr=ctx.sr))
        return float(np.std(ibi) / np.mean(ibi)) if ibi.mean() > 0 else 0.0
    return None


def _calc_beat_stability(ctx: AudioContext) -> float:
    bpm, beats = ctx.tempobeat
    if len(beats) > 1:
        ibi = np.diff(librosa.frames_to_time(beats, sr=ctx.sr))
        if len(ibi) > 0 and np.mean(ibi) > 0:
            cv = np.std(ibi) / np.mean(ibi)
            return float(1.0 / (1.0 + cv))
    return 0.0


def _calc_dominant_pitch(ctx: AudioContext) -> str:
    chroma_mean = np.mean(ctx.chroma, axis=1)
    return NOTES[int(np.argmax(chroma_mean))]


def _calc_spectral_centroid_mean(ctx: AudioContext) -> float:
    cent = librosa.feature.spectral_centroid(S=ctx.spectro, sr=ctx.sr)
    return float(np.mean(cent))


def _calc_spectral_centroid_sd(ctx: AudioContext) -> float:
    cent = librosa.feature.spectral_centroid(S=ctx.spectro, sr=ctx.sr)
    return float(np.std(cent))


def _calc_spectral_bandwidth(ctx: AudioContext) -> float:
    bw = librosa.feature.spectral_bandwidth(S=ctx.spectro, sr=ctx.sr)
    return float(np.mean(bw))


def _calc_flatness(ctx: AudioContext) -> float:
    flatness = librosa.feature.spectral_flatness(S=ctx.power)
    return float(np.mean(flatness))


def _calc_rolloff_features(ctx: AudioContext) -> SpectralRolloffFeatures:
    with LIBROSA_LOCK:
        rolloff = librosa.feature.spectral_rolloff(S=ctx.spectro, sr=ctx.sr)[0]
    if len(rolloff) == 0:
        return SpectralRolloffFeatures()
    mean_val = float(np.mean(rolloff))
    std_val = float(np.std(rolloff))
    seq = _resample_to_fixed_frames(rolloff, n=32)
    return SpectralRolloffFeatures(mean=mean_val, std=std_val, seq=seq)


def _calc_contrast(ctx: AudioContext) -> list[float]:
    contrast = librosa.feature.spectral_contrast(S=ctx.spectro, sr=ctx.sr)
    return [float(val) for val in np.mean(contrast, axis=1)]


def _calc_zcr_features(ctx: AudioContext) -> ZcrFeatures:
    with LIBROSA_LOCK:
        zcr = librosa.feature.zero_crossing_rate(y=ctx.y)[0]
    if len(zcr) == 0:
        return ZcrFeatures()
    mean_val = float(np.mean(zcr))
    std_val = float(np.std(zcr))
    seq = _resample_to_fixed_frames(zcr, n=32)
    return ZcrFeatures(mean=mean_val, std=std_val, seq=seq)


def _calc_snr(ctx: AudioContext) -> float | None:
    if ctx.source != "mix":
        return 0.0
    sig_pwr = np.mean(ctx.y**2)
    with LIBROSA_LOCK:
        noise_est = ctx.y - librosa.effects.preemphasis(ctx.y)
    noise_pwr = np.mean(noise_est**2)
    if noise_pwr > 0:
        return float(np.log10(sig_pwr / noise_pwr))
    return None


def _calc_mfccs(ctx: AudioContext) -> list[float]:
    log_mel = librosa.power_to_db(ctx.mel, ref=np.max)
    mfcc = librosa.feature.mfcc(S=log_mel, n_mfcc=8)
    return [float(val) for val in np.mean(mfcc, axis=1)]


def _calc_tonnetz(ctx: AudioContext) -> TonnetzFeatures | None:
    """Tonnetz和声空間特徴量を計算しますわ。

    drumsステムは調波成分が乏しく Tonnetz が無意味なため None を返しますの。
    seq は frame-major flatten: [f0_ax0..f0_ax5, f1_ax0..f1_ax5, ...]
    """
    if ctx.source == "drums":
        return None

    with LIBROSA_LOCK:
        t = librosa.feature.tonnetz(chroma=ctx.chroma_cqt)  # (6, T)
        delta_t = librosa.feature.delta(t)  # (6, T)

    mean = np.mean(t, axis=1).tolist()  # 6
    std = np.std(t, axis=1).tolist()  # 6
    delta_mean = np.mean(delta_t, axis=1).tolist()  # 6

    T_len = t.shape[1]
    x_new = np.linspace(0, 1, FIXED_SEQ_FRAMES)
    x_old = np.linspace(0, 1, T_len)
    seq_2d = np.stack(
        [np.interp(x_new, x_old, t[i]) for i in range(6)]
    )  # (6, FIXED_SEQ_FRAMES)
    # フレーム優先 (frame-major) flatten: (FIXED_SEQ_FRAMES, 6) → 192要素
    seq = seq_2d.T.flatten().tolist()

    return TonnetzFeatures(mean=mean, std=std, delta_mean=delta_mean, seq=seq)


def _calc_hnr_nap(ctx: AudioContext) -> float:
    """正規化自己相関ピーク (Normalized Autocorrelation Peak: NAP) による
    0.0〜1.0 の HNR 評価値を算出しますの。
    chunk処理でメモリ使用量を抑制しますわ。
    """
    if len(ctx.y) < 2048:
        return 0.0

    with LIBROSA_LOCK:
        lag_min = int(ctx.sr / 2000)
        lag_max_val = int(ctx.sr / 50)

        frame_len = 2048
        hop_len = 1024

        if len(ctx.y) < frame_len:
            y_pad = np.pad(ctx.y, (0, frame_len - len(ctx.y)))
            frames = y_pad[:, np.newaxis]
        else:
            frames = librosa.util.frame(
                ctx.y, frame_length=frame_len, hop_length=hop_len
            )

        x = frames.T
        n_frames, N = x.shape
        n_fft = 2 * N

        # chunk size: ~4096 frames per chunk (~16MB complex array)
        CHUNK = 4096
        lag_max = min(lag_max_val, N - 1)
        if lag_min >= lag_max:
            return 0.0

        all_naps = []
        all_valid = []

        for start in range(0, n_frames, CHUNK):
            end = min(start + CHUNK, n_frames)
            x_chunk = x[start:end]

            X_chunk = np.fft.rfft(x_chunk, n=n_fft, axis=-1)
            S_chunk = X_chunk * np.conj(X_chunk)
            r_chunk = np.fft.irfft(S_chunk, n=n_fft, axis=-1)[:, :N]

            r_0_chunk = r_chunk[:, 0:1]
            valid_mask = r_0_chunk[:, 0] > 1e-10

            r_norm_chunk = np.zeros_like(r_chunk)
            r_norm_chunk[valid_mask] = r_chunk[valid_mask] / r_0_chunk[valid_mask]

            r_search = r_norm_chunk[:, lag_min : lag_max + 1]
            naps_chunk = np.max(r_search, axis=-1)
            naps_chunk = np.clip(naps_chunk, 0.0, 1.0)

            all_naps.append(naps_chunk)
            all_valid.append(valid_mask)

        all_naps_arr = np.concatenate(all_naps)
        all_valid_arr = np.concatenate(all_valid)

        if np.any(all_valid_arr):
            hnr_val = float(np.mean(all_naps_arr[all_valid_arr]))
        else:
            hnr_val = 0.0

        return hnr_val


# ─────────────────────────────────────────────
# 新規特徴量計算関数群
# ─────────────────────────────────────────────
def _calc_section_features(ctx: AudioContext) -> SectionFeatures:
    """セクション構造特徴量を算出しますの。

    librosa onset_env の急変点検出により、音響的に均質なセクションを分割する。
    mix ステムのみ実行し、それ以外はデフォルト値を返す。

    SBic (BIC基準) はWindowsでEssentia Pythonが利用不可のため、
    onset_env の局所分散急変点を境界として代替実装しますの。
    """
    if ctx.source != "mix":
        return SectionFeatures()

    try:
        onset_env = ctx.onset_env  # cached (mel → onset_strength)

        if len(onset_env) < 20:
            return SectionFeatures()

        # ── Drop Position: onset_env の最大ピーク位置 ──
        drop_position = float(np.argmax(onset_env)) / max(len(onset_env), 1)

        # ── セクション境界検出: 局所分散の急変点をBIC風に検出 ──
        # 窓ごとのRMSを計算し、隣接窓間の変化量でセクション境界を推定する
        window = max(int(len(onset_env) * 0.05), 10)  # 全体の5% or 最小10フレーム
        n_frames = len(onset_env)

        # スライディング窓でローカルRMSを計算
        local_rms = np.array(
            [
                np.sqrt(np.mean(onset_env[max(0, i - window) : i + window] ** 2))
                for i in range(0, n_frames, window // 2)
            ]
        )

        if len(local_rms) < 3:
            return SectionFeatures(drop_position=drop_position)

        # 隣接差分の絶対値 → 上位 K 点を境界とみなす
        diffs = np.abs(np.diff(local_rms))
        duration_sec = len(ctx.y) / ctx.sr

        # セクション最小長を20秒と仮定してK上限を決める
        max_sections = max(1, int(duration_sec / 20))
        k = min(max_sections, len(diffs))

        if k < 1:
            return SectionFeatures(
                section_count=1,
                section_length_std=0.0,
                drop_position=drop_position,
            )

        # 上位k個の境界インデックス
        boundary_indices = np.argsort(diffs)[-k:]
        boundary_indices = np.sort(boundary_indices)

        # フレームインデックス → 秒 変換 (window//2 ホップ × hop_length / sr)
        hop_frames = window // 2
        hop_length = 512  # AudioContext の stft と同じ hop_length
        sec_per_frame = hop_frames * hop_length / ctx.sr

        boundary_secs = [float(b * sec_per_frame) for b in boundary_indices]
        boundary_secs = [b for b in boundary_secs if 0 < b < duration_sec]

        times = [0.0] + boundary_secs + [duration_sec]
        lengths = [times[i + 1] - times[i] for i in range(len(times) - 1)]

        section_count = len(lengths)
        section_length_std = float(np.std(lengths)) if len(lengths) > 1 else 0.0

        return SectionFeatures(
            section_count=section_count,
            section_length_std=section_length_std,
            drop_position=drop_position,
        )

    except Exception as e:
        logging.warning(
            f"[Section] セクション特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return SectionFeatures()


def _calc_groove_features(ctx: AudioContext) -> GrooveFeatures:
    """Groove / Syncopation 指標を算出しますの。

    librosa の beat_track + onset_detect を組み合わせ、
    Swing Ratio と Syncopation Index を計算する。

    参照: Witek et al. 2014 (PLoS ONE) / Longuet-Higgins & Lee (1984) metric weights
    mix ステムのみ実行し、それ以外はデフォルト値を返す。
    """
    if ctx.source != "mix":
        return GrooveFeatures()

    try:
        bpm, beats = ctx.tempobeat  # cached

        if len(beats) < 4:
            return GrooveFeatures()

        # ── Swing Ratio: 偶数/奇数 IBI 比 ──
        beat_times = librosa.frames_to_time(beats, sr=ctx.sr)
        ibi = np.diff(beat_times)  # Inter-Beat Intervals

        if len(ibi) >= 2:
            d1 = ibi[0::2]  # 偶数番目 (longer / on-beat)
            d2 = ibi[1::2]  # 奇数番目 (shorter / off-beat)
            min_len = min(len(d1), len(d2))
            if min_len > 0:
                SR = float(np.mean(d1[:min_len]) / (np.mean(d2[:min_len]) + 1e-10))
                # クランプ: 物理的に妥当な範囲 [0.5, 4.0]
                SR = float(np.clip(SR, 0.5, 4.0))
            else:
                SR = 1.0
        else:
            SR = 1.0

        # ── Syncopation Index: beat gridからの onset ズレ ──
        with LIBROSA_LOCK:
            onset_frames = librosa.onset.onset_detect(
                onset_envelope=ctx.onset_env, sr=ctx.sr
            )

        SI = 0.0
        if len(beats) > 1 and len(onset_frames) > 0:
            beat_period = float(np.mean(np.diff(beats)))  # フレーム単位のビート周期
            if beat_period > 0:
                # 各onsetから最近傍beatまでの距離を計算
                distances = np.array(
                    [
                        np.min(np.abs(onset_frames[i] - beats))
                        for i in range(len(onset_frames))
                    ],
                    dtype=float,
                )
                # ビート周期で正規化 → [0, 0.5] にクランプ (0=on-beat, 0.5=off-beat)
                phase = distances / beat_period
                SI = float(np.mean(np.minimum(phase, 1.0 - phase)))
                SI = float(np.clip(SI, 0.0, 0.5))

        # ── 3段階分類 ──
        if SR < 1.15:
            gc = "straight"
        elif SR < 1.7:
            gc = "swing"
        else:
            gc = "heavy_swing"

        return GrooveFeatures(
            swing_ratio=SR,
            syncopation_index=SI,
            groove_class=gc,
        )

    except Exception as e:
        logging.warning(f"[Groove] Groove特徴量算出エラー (source: {ctx.source}): {e}")
        return GrooveFeatures()


def _calc_crest_factor(ctx: AudioContext) -> float:
    """Crest Factor (クレストファクター) を算出しますの。

    CF = max|y| / sqrt(mean(y^2))
    高いほどダイナミックで打撃感あり、低いほど過剰コンプレッション。
    参照: EBU R128 / AES Standard
    """
    eps = 1e-10
    peak = float(np.max(np.abs(ctx.y)))
    rms = float(np.sqrt(np.mean(ctx.y**2)) + eps)
    return float(np.clip(peak / rms, 0.0, 100.0))


def _calc_temporal_seq(ctx: AudioContext) -> TemporalSeqFeatures | None:
    """固定フレーム (FIXED_SEQ_FRAMES=32) 時系列特徴量を算出しますの。
    すべてのステムで動作しますわ。
    """
    try:
        # ── RMS 軌跡 ──
        if ctx.source == "mix":
            rms = librosa.feature.rms(S=ctx.spectro)[0]
        else:
            with LIBROSA_LOCK:
                rms = librosa.feature.rms(y=ctx.y, frame_length=2048, hop_length=512)[0]

        # ── 局所クレストファクター (Dynamics Range) 軌跡 ──
        y_pad = np.pad(ctx.y, 1024, mode="constant")
        from numpy.lib.stride_tricks import sliding_window_view

        y_frames = sliding_window_view(y_pad, 2048)[::512]

        peaks = np.max(np.abs(y_frames), axis=1)
        min_len = min(len(peaks), len(rms))
        peaks = peaks[:min_len]
        rms_aligned = rms[:min_len]

        crest_seq = peaks / (rms_aligned + 1e-10)
        crest_seq = np.clip(crest_seq, 0.0, 100.0)
        dynamics_range_seq = _resample_to_fixed_frames(crest_seq)

        # ── Spectral Centroid 軌跡 (キャッシュプロパティを利用しますわ) ──
        centroid = ctx.centroid  # (T,)

        # ── Chroma Shannon Entropy 軌跡 ──
        chroma = ctx.chroma  # (12, T)
        p = chroma / (chroma.sum(axis=0, keepdims=True) + 1e-10)
        entropy = -np.sum(p * np.log2(p + 1e-10), axis=0)  # (T,)

        # ── Spectral Centroid Delta 軌跡 ──
        with LIBROSA_LOCK:
            delta = librosa.feature.delta(centroid)  # (T,)

        centroid_mean = float(np.mean(centroid))
        centroid_std = float(np.std(centroid))
        centroid_seq = _resample_to_fixed_frames(centroid)
        chroma_entropy_mean = float(np.mean(entropy))
        chroma_entropy_std = float(np.std(entropy))
        chroma_entropy_seq = _resample_to_fixed_frames(entropy)
        centroid_delta_mean = float(np.mean(delta))
        centroid_delta_std = float(np.std(delta))
        centroid_delta_seq = _resample_to_fixed_frames(delta)

        return TemporalSeqFeatures(
            centroid_mean=centroid_mean,
            centroid_std=centroid_std,
            centroid_seq=centroid_seq,
            rms_seq=_resample_to_fixed_frames(rms),
            chroma_entropy_mean=chroma_entropy_mean,
            chroma_entropy_std=chroma_entropy_std,
            chroma_entropy_seq=chroma_entropy_seq,
            centroid_delta_mean=centroid_delta_mean,
            centroid_delta_std=centroid_delta_std,
            centroid_delta_seq=centroid_delta_seq,
            dynamics_range_seq=dynamics_range_seq,
        )

    except Exception as e:
        logging.warning(
            f"[TemporalSeq] 時系列特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return None


def _calc_key_features(ctx: AudioContext) -> KeyFeatures:
    """Krumhansl-Schmucklerキー推定アルゴリズムを用いてキー特徴量を算出しますの。
    mix ステムのみ実行し、それ以外はデフォルト値を返しますわ。
    """
    if ctx.source != "mix":
        return KeyFeatures()

    try:
        # chroma_cqtキャッシュを再利用
        chroma = ctx.chroma_cqt  # (12, T)
        if chroma.shape[1] == 0:
            return KeyFeatures()

        # 時間平均クロマベクトル
        chroma_mean = np.mean(chroma, axis=1)

        # クロマベクトルの正規化
        chroma_std = np.std(chroma_mean)
        if chroma_std > 1e-10:
            chroma_norm = (chroma_mean - np.mean(chroma_mean)) / chroma_std
        else:
            chroma_norm = chroma_mean - np.mean(chroma_mean)

        best_corr = -2.0
        best_key = 0
        best_scale = "major"

        # 24のテンプレートとのピアソン相関係数を計算
        for scale_name in ["major", "minor"]:
            profile = np.array(KEY_PROFILES[scale_name])
            # プロファイルの正規化
            prof_norm = (profile - np.mean(profile)) / (np.std(profile) + 1e-10)

            for shift in range(12):
                t_rotated = np.roll(prof_norm, shift)
                # ピアソン相関係数
                corr = float(np.dot(chroma_norm, t_rotated) / 12.0)
                if corr > best_corr:
                    best_corr = corr
                    best_key = shift
                    best_scale = scale_name

        key_name = NOTES[best_key]

        # ── 時系列 KeyStrengthSeq ──
        # 各フレームごとに、決定されたグローバルキーに対応するテンプレートとの相関係数を計算
        target_profile = np.array(KEY_PROFILES[best_scale])
        target_prof_norm = np.roll(
            (target_profile - np.mean(target_profile))
            / (np.std(target_profile) + 1e-10),
            best_key,
        )

        T_len = chroma.shape[1]
        corrs_t = []
        for t in range(T_len):
            c_t = chroma[:, t]
            c_t_std = np.std(c_t)
            if c_t_std > 1e-10:
                c_t_norm = (c_t - np.mean(c_t)) / c_t_std
            else:
                c_t_norm = c_t - np.mean(c_t)
            corr_t = float(np.dot(c_t_norm, target_prof_norm) / 12.0)
            corrs_t.append(corr_t)

        corrs_t_arr = np.array(corrs_t)
        # 他特徴量と同様に32フレームでリサンプル
        seq = _resample_to_fixed_frames(corrs_t_arr, n=FIXED_SEQ_FRAMES)
        mean_seq = float(np.mean(corrs_t_arr))
        std_seq = float(np.std(corrs_t_arr))

        return KeyFeatures(
            key=key_name,
            scale=best_scale,
            key_strength=float(np.clip(best_corr, -1.0, 1.0)),
            key_strength_mean=mean_seq,
            key_strength_std=std_seq,
            key_strength_seq=seq,
        )

    except Exception as e:
        logging.warning(f"[Key] キー特徴量算出エラー (source: {ctx.source}): {e}")
        return KeyFeatures()


def _calc_tempogram_features(ctx: AudioContext) -> TempogramFeatures:
    """Tempogramからテンポ変動分布の統計量および時系列(TempoSeq)を計算しますの。
    すべてのステムで動作しますわ。
    """
    try:
        tempogram = ctx.tempogram  # cached (onset_env → tempogram)

        if tempogram.size == 0:
            return TempogramFeatures()

        mean_val = float(np.mean(tempogram))
        std_val = float(np.std(tempogram))

        # 各フレームにおける最大値の平均
        peak_val = float(np.mean(np.max(tempogram, axis=0)))

        # 各フレームを確率分布とみなした時のエントロピーの平均値
        p = tempogram / (np.sum(tempogram, axis=0, keepdims=True) + 1e-10)
        entropy = -np.sum(p * np.log2(p + 1e-10), axis=0)
        entropy_val = float(np.mean(entropy))

        # ── 支配的テンポ（BPM）軌跡 TempoSeq の算出 ──
        # インデックス0 (BPM=inf) を除外して、インデックス1以降から最大値を探索しますわ
        best_bins = np.argmax(tempogram[1:], axis=0) + 1  # (t_frames,)
        with LIBROSA_LOCK:
            frequencies = librosa.tempo_frequencies(
                tempogram.shape[0], sr=ctx.sr, hop_length=512
            )
        tempo_seq_bpm = frequencies[best_bins]
        tempo_seq = _resample_to_fixed_frames(tempo_seq_bpm)

        return TempogramFeatures(
            mean=mean_val,
            std=std_val,
            peak=peak_val,
            entropy=entropy_val,
            tempo_seq=tempo_seq,
        )

    except Exception as e:
        logging.warning(
            f"[Tempogram] テンポグラム特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return TempogramFeatures()


def _calc_onset_features(ctx: AudioContext) -> OnsetFeatures:
    """Onset Strength 系列から統計量および自己相関によるリズムDNAを計算しますの。
    mix ステムのみ実行し、それ以外はデフォルト値を返しますわ。
    """
    if ctx.source != "mix":
        return OnsetFeatures()

    try:
        onset_env = ctx.onset_env  # cached

        if len(onset_env) == 0:
            return OnsetFeatures()

        mean_val = float(np.mean(onset_env))
        std_val = float(np.std(onset_env))
        max_val = float(np.max(onset_env))

        p25 = float(np.percentile(onset_env, 25))
        p50 = float(np.percentile(onset_env, 50))
        p75 = float(np.percentile(onset_env, 75))

        crest = max_val / (mean_val + 1e-10)

        # 歪度 (skew) と 尖度 (kurt) の計算
        diff = onset_env - mean_val
        if std_val > 1e-10:
            skew_val = float(np.mean(diff**3) / (std_val**3))
            kurt_val = float(np.mean(diff**4) / (std_val**4) - 3.0)
        else:
            skew_val = 0.0
            kurt_val = 0.0

        # オンセット強度の自己相関 (最大サイズ 160 フレーム、約 3.7 秒)
        with LIBROSA_LOCK:
            ac = librosa.autocorrelate(onset_env, max_size=160)

        # 16bin への圧縮（線形リサンプル）
        autocorr_seq = _resample_to_fixed_frames(ac, n=16)

        # 32フレーム固定長 raw 1次元時系列の算出
        onset_strength_seq = _resample_to_fixed_frames(onset_env, n=32)

        return OnsetFeatures(
            mean=mean_val,
            std=std_val,
            max=max_val,
            p25=p25,
            p50=p50,
            p75=p75,
            crest=crest,
            autocorr=autocorr_seq,
            skew=skew_val,
            kurt=kurt_val,
            onset_strength_seq=onset_strength_seq,
        )

    except Exception as e:
        logging.warning(
            f"[Onset] オンセット強度特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return OnsetFeatures()


# ─────────────────────────────────────────────
# FeatureExtractor インスタンス群
# ─────────────────────────────────────────────
extract_rms_mean = FeatureExtractor(_calc_rms_mean, "rms_mean")
extract_rms_peak = FeatureExtractor(_calc_rms_peak, "rms_peak")
extract_energy = FeatureExtractor(_calc_energy, "energy")
extract_bpm = FeatureExtractor(_calc_bpm, "bpm")
extract_beat_regularity = FeatureExtractor(_calc_beat_regularity, "beat_regularity")
extract_beat_stability = FeatureExtractor(_calc_beat_stability, "beat_stability")
extract_dominant_pitch = FeatureExtractor(_calc_dominant_pitch, "dominant_pitch")
extract_spectral_centroid_mean = FeatureExtractor(
    _calc_spectral_centroid_mean, "spectral_centroid_mean"
)
extract_spectral_centroid_sd = FeatureExtractor(
    _calc_spectral_centroid_sd, "spectral_centroid_sd"
)
extract_spectral_bandwidth = FeatureExtractor(
    _calc_spectral_bandwidth, "spectral_bandwidth"
)
extract_flatness = FeatureExtractor(_calc_flatness, "flatness")
extract_rolloff = FeatureExtractor(_calc_rolloff_features, "rolloff")
extract_contrast = FeatureExtractor(_calc_contrast, "contrast")
extract_zcr = FeatureExtractor(_calc_zcr_features, "zcr")
extract_snr = FeatureExtractor(_calc_snr, "snr")
extract_hnr = FeatureExtractor(lambda ctx: ctx.hnr, "hnr")
extract_mfccs = FeatureExtractor(_calc_mfccs, "mfccs")
extract_tonnetz = FeatureExtractor(_calc_tonnetz, "tonnetz")
# 新規
extract_section = FeatureExtractor(_calc_section_features, "section")
extract_groove = FeatureExtractor(_calc_groove_features, "groove")
extract_crest_factor = FeatureExtractor(_calc_crest_factor, "crest_factor")
extract_temporal_seq = FeatureExtractor(_calc_temporal_seq, "temporal_seq")
extract_key = FeatureExtractor(_calc_key_features, "key")
extract_tempogram = FeatureExtractor(_calc_tempogram_features, "tempogram")
extract_onset = FeatureExtractor(_calc_onset_features, "onset")
extract_rms_obj = FeatureExtractor(_calc_rms_features, "rms_obj")
extract_centroid_obj = FeatureExtractor(_calc_centroid_features, "centroid_obj")
extract_mfcc_obj = FeatureExtractor(_calc_mfcc_features, "mfcc_obj")
extract_chroma_obj = FeatureExtractor(_calc_chroma_features, "chroma_obj")


# ─────────────────────────────────────────────
# librosa_extractor → RawFeatures 変換 (v4)
# ─────────────────────────────────────────────
def _build_raw_features(
    rms_mean,
    rms_peak,
    energy,
    bpm,
    beat_regularity,
    beat_stability,
    dominant_pitch,
    spectral_centroid_mean,
    spectral_centroid_sd,
    spectral_bandwidth,
    flatness,
    rolloff,
    contrast,
    zcr,
    snr,
    hnr,
    mfccs,
    tonnetz,
    section,
    groove,
    crest_factor,
    temporal_seq,
    key_feat,
    tempogram_feat,
    onset_feat,
    rms_obj,
    centroid_obj,
    mfcc_obj,
    chroma_obj,
    ctx: AudioContext,
) -> RawFeatures:
    """Product合成結果から RawFeatures を構築しますわ！"""
    # RMS 7スカラー
    rms_stats = _calc_rms_stats(rms_obj.seq) if rms_obj else {}
    # Centroid 7スカラー
    centroid_stats = _calc_centroid_stats(centroid_obj.seq) if centroid_obj else {}

    # 2次元シーケンスのリサンプル
    tonnetz_seq_2d = []
    if tonnetz is not None:
        t = tonnetz.seq  # frame-major flatten (192要素)
        tonnetz_seq_2d = [t[i * 32 : (i + 1) * 32] for i in range(6)]

    chroma_seq_2d = []
    if chroma_obj is not None:
        chroma_seq_2d = chroma_obj.seq  # (12, T) → リサンプル済み

    mfcc_seq_2d = []
    if mfcc_obj is not None:
        mfcc_seq_2d = mfcc_obj.seq  # (8, T) → リサンプル済み

    # chord_sequence / vocal_f0_seq
    chord_seq = _calc_chord_sequence(ctx)
    f0_seq = _calc_vocal_f0_seq(ctx) if ctx.source == "vocals" else None

    # rolloff_feat (rolloff 引数は SpectralRolloffFeatures 型ですわ)
    rolloff_mean_val = rolloff.mean if rolloff else 0.0
    rolloff_std_val = rolloff.std if rolloff else 0.0
    rolloff_seq_val = rolloff.seq if rolloff else []

    # zcr_feat (zcr 引数は ZcrFeatures 型ですわ)
    zcr_mean_val = zcr.mean if zcr else 0.0
    zcr_std_val = zcr.std if zcr else 0.0
    zcr_seq_val = zcr.seq if zcr else []

    return RawFeatures(
        energy=energy,
        bpm=float(bpm) if bpm else 0.0,
        crest_factor=crest_factor,
        snr=snr,
        hnr=hnr,
        # RMS 7スカラー
        rms_mean=rms_stats.get("mean", float(rms_mean)),
        rms_std=rms_stats.get("std", float(np.std(rms_obj.seq) if rms_obj else 0)),
        rms_peak=rms_stats.get("peak", float(rms_peak)),
        rms_max=rms_stats.get("max", float(rms_peak)),
        rms_min=rms_stats.get("min", 0.0),
        rms_median=rms_stats.get("median", 0.0),
        rms_entropy=rms_stats.get("entropy", 0.0),
        # Centroid 7スカラー
        centroid_mean=centroid_stats.get("mean", float(spectral_centroid_mean)),
        centroid_std=centroid_stats.get("std", float(spectral_centroid_sd)),
        centroid_peak=centroid_stats.get("peak", 0.0),
        centroid_max=centroid_stats.get("max", 0.0),
        centroid_min=centroid_stats.get("min", 0.0),
        centroid_median=centroid_stats.get("median", 0.0),
        centroid_entropy=centroid_stats.get("entropy", 0.0),
        # 復元メンバ
        beat_regularity=beat_regularity,
        dominant_pitch=dominant_pitch,
        onset_feat=onset_feat,
        tempogram_feat=tempogram_feat,
        # 新規追加メンバ
        rolloff_mean=rolloff_mean_val,
        rolloff_std=rolloff_std_val,
        rolloff_seq=rolloff_seq_val,
        beat_stability=beat_stability,
        # 復元追加メンバ (以前の不整合解消)
        spectral_bandwidth=float(spectral_bandwidth),
        flatness=float(flatness),
        zcr_mean=zcr_mean_val,
        zcr_std=zcr_std_val,
        zcr_seq=zcr_seq_val,
        contrast_bands=[float(val) for val in contrast],
        mfccs=[float(val) for val in mfccs],
        # 構造/調性
        section=section if isinstance(section, SectionFeatures) else SectionFeatures(),
        groove=groove if isinstance(groove, GrooveFeatures) else GrooveFeatures(),
        key_feat=key_feat if isinstance(key_feat, KeyFeatures) else KeyFeatures(),
        # シーケンス
        rms_seq=temporal_seq.rms_seq if temporal_seq else [],
        centroid_seq=temporal_seq.centroid_seq if temporal_seq else [],
        centroid_delta_seq=temporal_seq.centroid_delta_seq if temporal_seq else [],
        dynamics_range_seq=temporal_seq.dynamics_range_seq if temporal_seq else [],
        tempogram_tempo=tempogram_feat.tempo_seq
        if isinstance(tempogram_feat, TempogramFeatures)
        else [],
        key_strength_seq=key_feat.key_strength_seq
        if isinstance(key_feat, KeyFeatures)
        else [],
        tonnetz=tonnetz_seq_2d,
        chroma=chroma_seq_2d,
        mfcc=mfcc_seq_2d,
        chord_sequence=chord_seq,
        vocal_f0_seq=f0_seq,
    )


# ─────────────────────────────────────────────
# librosa_extractor: Product合成 (v4 → RawFeatures)
# ─────────────────────────────────────────────
_librosa_product = product_all(
    extract_rms_mean,
    extract_rms_peak,
    extract_energy,
    extract_bpm,
    extract_beat_regularity,
    extract_beat_stability,
    extract_dominant_pitch,
    extract_spectral_centroid_mean,
    extract_spectral_centroid_sd,
    extract_spectral_bandwidth,
    extract_flatness,
    extract_rolloff,
    extract_contrast,
    extract_zcr,
    extract_snr,
    extract_hnr,
    extract_mfccs,
    extract_tonnetz,
    # 新規
    extract_section,
    extract_groove,
    extract_crest_factor,
    extract_temporal_seq,
    extract_key,
    extract_tempogram,
    extract_onset,
    # 詳細オブジェクト (7スカラー計算用)
    extract_rms_obj,
    extract_centroid_obj,
    extract_mfcc_obj,
    extract_chroma_obj,
)

librosa_extractor_v4: FeatureExtractor[RawFeatures] = FeatureExtractor(
    lambda ctx: _build_raw_features(*_librosa_product.run(ctx), ctx=ctx),
    "librosa_extractor_v4",
)


# 互換性用のエイリアス
librosa_extractor: FeatureExtractor[RawFeatures] = librosa_extractor_v4


@dataclass
class StemFeatures:
    """各ステムから抽出する最小限の特徴量を保持するデータクラスですわ。"""

    energy: float = 0.0
    zcr: float = 0.0
    hnr: float = 0.0
    spectral_centroid_mean: float = 0.0
    rms_mean: float = 0.0


# 互換性用のエイリアス
stem_extractor: FeatureExtractor[RawFeatures] = librosa_extractor


# ─────────────────────────────────────────────
# STEM_CONFIGS
# 各ステムに対する事前評価(Pre-warming)対象プロパティと、
# 適用するFeatureExtractorを定義しますわ。
# ─────────────────────────────────────────────
STEM_CONFIGS: dict[str, dict[str, Any]] = {
    "mix": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "drums": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "bass": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "vocals": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "other": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "guitar": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "piano": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
}
