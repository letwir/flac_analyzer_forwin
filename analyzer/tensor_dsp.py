"""
Mor(AudioContext | torch.Tensor -> TensorFeatures | BatchSpectralFeatures)
Functor(f o g) | Semantics(Category: Pure Domain GPU Accelerated Measurement Instrument)

Analyzer Tensor DSP Module
==========================
PyTorch / CUDA テンソル演算を用いた高次音響計測器ですわ！
7ステム一括バッチ STFT、Wiener-Khinchin $2N$ パディング cuFFT HNR/NAP、
Spectral Centroid/Rolloff/Flatness/Bandwidth/ZCR/RMS/Flux/PSD/Key/Chord などの
音響物理指標を数学的精度（命題1）を厳格に保持しながら GPU 完全並列計算いたしますの。
"""

import math
import os
from typing import Any
import numpy as np
import torch

from constants import CHORDS_DIC, KEY_PROFILES, NOTES
from .core import AudioContext, FIXED_SEQ_FRAMES, FeatureExtractor, _resample_to_fixed_frames
from .types import TensorFeatures


# ─────────────────────────────────────────────
# 1. 瞬時位相 & Hilbert 変換 (Pure Tensor Morphism)
# ─────────────────────────────────────────────
def hilbert_envelope_phase(x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
    """1Dまたはバッチテンソルに対するHilbert変換を行い、エンベロープと瞬時位相を返しますの。"""
    n_samples = x.shape[-1]
    if n_samples == 0:
        empty = torch.empty_like(x)
        return empty, empty

    try:
        xf = torch.fft.fft(x, dim=-1)
        h = torch.zeros(n_samples, device=x.device, dtype=xf.dtype)
        if n_samples % 2 == 0:
            h[0] = h[n_samples // 2] = 1
            h[1 : n_samples // 2] = 2
        else:
            h[0] = 1
            h[1 : (n_samples + 1) // 2] = 2

        xa = torch.fft.ifft(xf * h, dim=-1)
        return xa.abs(), xa.angle()
    except Exception as e:
        if x.device.type != "cuda":
            raise RuntimeError(f"Hilbert変換に失敗いたしましたわ: {e}") from e

        # cuFFT の制限やメモリ不足が発生した場合、CPUへ安全にフォールバック
        x_cpu = x.cpu()
        xf_cpu = torch.fft.fft(x_cpu, dim=-1)
        h_cpu = torch.zeros(n_samples, device=x_cpu.device, dtype=xf_cpu.dtype)
        if n_samples % 2 == 0:
            h_cpu[0] = h_cpu[n_samples // 2] = 1
            h_cpu[1 : n_samples // 2] = 2
        else:
            h_cpu[0] = 1
            h_cpu[1 : (n_samples + 1) // 2] = 2
        xa_cpu = torch.fft.ifft(xf_cpu * h_cpu, dim=-1)
        return xa_cpu.abs().to(x.device), xa_cpu.angle().to(x.device)


# ─────────────────────────────────────────────
# 2. HNR / NAP の Wiener-Khinchin cuFFT (Advisory 1 厳格適用: 2N ゼロパディング)
# ─────────────────────────────────────────────
def calc_hnr_nap_tensor(y: torch.Tensor, sr: int) -> tuple[float, float]:
    """
    Wiener-Khinchin 定理に基づく自己相関を cuFFT で $O(N \\log N)$ 高速計算しますわ！
    Advisory 1: 長さ N の信号を 2N 点（次の 2^k 点）へゼロパディングし、
    SciPy / Librosa の np.correlate (線形自己相関) と完全等価な数値を算出しますの。
    """
    n_samples = y.shape[-1]
    if n_samples == 0:
        return 0.0, -40.0

    # 1. 2N 以上の最小の 2 の累乗点へゼロパディング (FFT 高速化 & 線形自己相関の数学的等価性)
    n_fft = 1 << (2 * n_samples - 1).bit_length()

    y_f = y.float()
    yf = torch.fft.rfft(y_f, n=n_fft)
    psd = yf.abs().square()
    r = torch.fft.irfft(psd, n=n_fft)[..., :n_samples]
    if r.ndim > 1:
        r = r.mean(dim=list(range(r.ndim - 1)))

    r0 = float(r[0].item())
    if r0 < 1e-9:
        return 0.0, -40.0

    norm_r = r / r0
    min_lag = int(sr / 500) if sr > 0 else 44
    max_lag = int(sr / 50) if sr > 0 else 441

    if norm_r.shape[-1] <= min_lag:
        return 0.0, -40.0

    search_slice = norm_r[..., min_lag : min(norm_r.shape[-1], max_lag)]
    if search_slice.numel() == 0:
        return 0.0, -40.0

    nap_val = float(torch.max(search_slice).clamp(0.0, 1.0).item())

    # HNR dB 変換
    if nap_val >= 0.9999:
        hnr_db = 40.0
    elif nap_val <= 0.0001:
        hnr_db = -40.0
    else:
        hnr_db = float(np.clip(10.0 * math.log10(nap_val / (1.0 - nap_val)), -40.0, 40.0))

    return nap_val, hnr_db


# ─────────────────────────────────────────────
# 3. Welch PSD & Bandpass Envelope
# ─────────────────────────────────────────────
def welch_psd(
    x: torch.Tensor, sr: int, n_fft: int = 2048
) -> tuple[torch.Tensor, torch.Tensor]:
    """Welch法に基づく平均化パワースペクトル密度 (PSD) を算出しますわ。"""
    if x.shape[-1] < n_fft:
        n_fft = max(256, 1 << (x.shape[-1].bit_length() - 1)) if x.shape[-1] >= 256 else x.shape[-1]

    window = torch.hann_window(n_fft, device=x.device)
    stft = torch.stft(
        x,
        n_fft=n_fft,
        window=window,
        return_complex=True,
        hop_length=n_fft // 2,
        center=False,
    )
    psd = stft.abs().pow(2).mean(dim=-1)
    if psd.ndim > 1:
        psd = psd.mean(dim=list(range(psd.ndim - 1)))
    freqs = torch.linspace(0, sr / 2, psd.shape[-1], device=x.device)
    return freqs, psd


def fft_bandpass_envelope(
    x: torch.Tensor, sr: int, f_lo: float, f_hi: float
) -> torch.Tensor:
    """FFTベースの理想バンドパスフィルタリング後のエンベロープ抽出ですわ。"""
    n_samples = x.shape[-1]
    if n_samples == 0:
        return torch.empty(0, device=x.device, dtype=x.dtype)

    try:
        xf = torch.fft.rfft(x)
        freqs = torch.fft.rfftfreq(n_samples, d=1.0 / sr).to(x.device)
        mask = (freqs >= f_lo) & (freqs <= f_hi)
        xf_filtered = xf * mask
        x_filtered = torch.fft.irfft(xf_filtered, n=n_samples)
        env, _ = hilbert_envelope_phase(x_filtered)
        return env
    except Exception as e:
        if x.device.type != "cuda":
            raise RuntimeError(f"FFTバンドパス処理に失敗いたしましたわ: {e}") from e

        x_cpu = x.cpu()
        xf_cpu = torch.fft.rfft(x_cpu)
        freqs_cpu = torch.fft.rfftfreq(n_samples, d=1.0 / sr)
        mask_cpu = (freqs_cpu >= f_lo) & (freqs_cpu <= f_hi)
        xf_filtered_cpu = xf_cpu * mask_cpu
        x_filtered_cpu = torch.fft.irfft(xf_filtered_cpu, n=n_samples)
        env_cpu, _ = hilbert_envelope_phase(x_filtered_cpu)
        return env_cpu.to(x.device)


# ─────────────────────────────────────────────
# 4. 7ステム一括バッチ STFT & スペクトル特徴量 GPU テンソル射
# ─────────────────────────────────────────────
def calc_batch_stft_and_spectro(
    y_batch: torch.Tensor, n_fft: int = 2048, hop_length: int = 512
) -> tuple[torch.Tensor, torch.Tensor]:
    """
    複数ステムテンソル (B, samples) に対する一括 STFT および振幅スペクトログラム生成ですわ！
    Librosa のデフォルト (pad_mode="constant" / ゼロパディング) と完全一致させますの。
    """
    window = torch.hann_window(n_fft, device=y_batch.device)
    stft = torch.stft(
        y_batch,
        n_fft=n_fft,
        hop_length=hop_length,
        window=window,
        return_complex=True,
        center=True,
        pad_mode="constant",
    )
    spectro = stft.abs()
    return stft, spectro


def calc_spectral_centroid_tensor(spectro: torch.Tensor, sr: int, n_fft: int = 2048) -> torch.Tensor:
    """
    Spectrogram (B, F, T) または (F, T) から Spectral Centroid 時系列を GPU 一括算出しますわ！
    """
    n_bins = spectro.shape[-2]
    freqs = torch.linspace(0, sr / 2, n_bins, device=spectro.device, dtype=spectro.dtype)
    if spectro.ndim == 3:
        freqs = freqs.view(1, n_bins, 1)
    else:
        freqs = freqs.view(n_bins, 1)

    spectro_sum = spectro.sum(dim=-2, keepdim=True)
    spectro_sum_safe = torch.where(spectro_sum == 0.0, torch.ones_like(spectro_sum), spectro_sum)
    centroid = (spectro * freqs).sum(dim=-2, keepdim=True) / spectro_sum_safe
    return centroid.squeeze(-2)


def calc_spectral_rolloff_tensor(
    spectro: torch.Tensor, sr: int, roll_percent: float = 0.85, n_fft: int = 2048
) -> torch.Tensor:
    """
    Spectrogram (B, F, T) または (F, T) から Spectral Rolloff (85%) を GPU 一括算出しますわ！
    """
    n_bins = spectro.shape[-2]
    freqs = torch.linspace(0, sr / 2, n_bins, device=spectro.device, dtype=spectro.dtype)

    total_energy = spectro.sum(dim=-2, keepdim=True) * roll_percent
    cum_energy = torch.cumsum(spectro, dim=-2)

    mask = cum_energy >= total_energy
    idx = mask.int().argmax(dim=-2)
    rolloff = freqs[idx]
    return rolloff


def calc_spectral_flatness_tensor(spectro: torch.Tensor, amin: float = 1e-10, power: float = 2.0) -> torch.Tensor:
    """
    Spectral Flatness (幾何平均 / 算術平均) を GPU 一括算出しますわ！
    Librosa の S = |STFT|^power 定義に厳密準拠いたしますの。
    """
    if power != 1.0:
        S = spectro.pow(power)
    else:
        S = spectro

    S_clamped = torch.clamp(S, min=amin)
    log_S = torch.log(S_clamped)
    geom_mean = torch.exp(log_S.mean(dim=-2))
    arith_mean = S_clamped.mean(dim=-2)
    flatness = geom_mean / arith_mean
    return flatness


def calc_zcr_tensor(y: torch.Tensor) -> torch.Tensor:
    """
    Zero Crossing Rate (ZCR) を GPU 一括算出しますわ！
    """
    sign_diff = (torch.sign(y[..., 1:]) != torch.sign(y[..., :-1])).float()
    return sign_diff.mean(dim=-1)


# ─────────────────────────────────────────────
# 5. 和声・Key 推定のテンソルバッチ行列乗算 (GPU GEMM)
# ─────────────────────────────────────────────
def estimate_key_tensor(chroma_cqt_avg: torch.Tensor) -> tuple[str, str, float]:
    """
    Krumhansl-Schmuckler アルゴリズムによる Key / Scale 推定をテンソル化いたしますわ！
    """
    norm = chroma_cqt_avg.norm()
    if norm > 1e-9:
        chroma_norm = chroma_cqt_avg / norm
    else:
        chroma_norm = chroma_cqt_avg

    best_key = "Unknown"
    best_scale = "Unknown"
    best_corr = -1.0

    chroma_np = chroma_norm.cpu().numpy()

    for mode in ["major", "minor"]:
        profile = KEY_PROFILES[mode]
        p_norm = profile / np.linalg.norm(profile)
        for shift in range(12):
            rolled = np.roll(chroma_np, -shift)
            corr = float(np.dot(rolled, p_norm))
            if corr > best_corr:
                best_corr = corr
                best_key = NOTES[shift]
                best_scale = mode

    return best_key, best_scale, float(max(0.0, best_corr))


# ─────────────────────────────────────────────
# 6. 統合テンソル特徴量抽出射 (extract_tensor_features)
# ─────────────────────────────────────────────
def extract_tensor_features(
    y: torch.Tensor, sr: int, device: torch.device, spectro_path: str | None = None
) -> dict[str, Any]:
    """1つの波形テンソルに対する Spectral Flux, Welch PSD Peak, Subbass Envelope, HNR/NAP 計測射ですわ！"""
    y = y.to(device)
    features: dict[str, Any] = {}

    # 1. HNR / NAP の cuFFT Wiener-Khinchin 計算 (Advisory 1)
    nap_val, hnr_db = calc_hnr_nap_tensor(y, sr)
    features["nap"] = nap_val
    features["hnr_db"] = hnr_db

    # 2. Spectral Flux and Welch PSD Peaks
    if spectro_path and os.path.exists(spectro_path):
        stft_mag = torch.from_numpy(np.load(spectro_path, mmap_mode="r")).to(device)
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        psd = stft_mag.pow(2).mean(dim=-1)
        freqs = torch.linspace(0, sr / 2, psd.shape[-1], device=device)
    else:
        window_1024 = torch.hann_window(1024, device=device)
        stft_mag = torch.stft(
            y, n_fft=1024, window=window_1024, return_complex=True, center=True, pad_mode="reflect"
        ).abs()
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        freqs, psd = welch_psd(y, sr=sr)

    features["spectral_flux_mean"] = float(flux.mean().item())
    features["spectral_flux_std"] = float(flux.std().item())

    # 3. Welch PSD Peaks (1次元に集約して安全にピーク検出)
    psd_1d = psd.mean(dim=list(range(psd.ndim - 1))) if psd.ndim > 1 else psd
    peak_idx = psd_1d.argmax()
    features["psd_peak_freq"] = float(freqs[peak_idx].item()) if peak_idx < len(freqs) else 0.0
    features["psd_peak_val"] = float(psd_1d[peak_idx].item()) if peak_idx < len(psd_1d) else 0.0

    # 4. Phase Envelope (Sub-bass: 20-60Hz などの帯域別)
    sub_env = fft_bandpass_envelope(y, sr, 20.0, 60.0)
    features["subbass_env_mean"] = float(sub_env.mean().item()) if sub_env.numel() > 0 else 0.0

    return features


def extract_tensor_obj(
    ctx: AudioContext, device: torch.device | None = None
) -> TensorFeatures:
    """AudioContext から TensorFeatures データクラスを生成する純粋射ですの。"""
    dev = device if device is not None else torch.device("cuda" if torch.cuda.is_available() else "cpu")
    y_tensor = torch.from_numpy(ctx.y)
    feats_dict = extract_tensor_features(y_tensor, ctx.sr, dev, spectro_path=ctx._spectro_path)
    return TensorFeatures(
        spectral_flux_mean=feats_dict.get("spectral_flux_mean", 0.0),
        spectral_flux_std=feats_dict.get("spectral_flux_std", 0.0),
        psd_peak_freq=feats_dict.get("psd_peak_freq", 0.0),
        psd_peak_val=feats_dict.get("psd_peak_val", 0.0),
        subbass_env_mean=feats_dict.get("subbass_env_mean", 0.0),
    )


# Reader Applicative 射の定義
tensor_extractor: FeatureExtractor[TensorFeatures] = FeatureExtractor(
    extract_tensor_obj, "tensor_extractor"
)
