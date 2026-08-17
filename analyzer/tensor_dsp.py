"""
Mor(AudioContext | torch.Tensor -> TensorFeatures)
Functor(f o g) | Semantics(Category: Pure Domain Measurement Instrument)

Analyzer Tensor DSP Module
==========================
PyTorch / CUDA テンソル演算を用いた高次音響計測器ですわ！
瞬時位相 (Instantaneous Phase)、Welch PSD、Spectral Flux、
帯域別 Envelope (Sub-bass) などの物理音響指標を純粋計算いたしますの。
"""

import os
import torch
import numpy as np
from typing import Any

from .core import AudioContext, FeatureExtractor
from .types import TensorFeatures


def hilbert_envelope_phase(x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
    """1Dテンソルに対するHilbert変換を行い、エンベロープと瞬時位相を返しますの。"""
    n_samples = x.shape[-1]
    if n_samples == 0:
        empty = torch.empty(0, device=x.device, dtype=x.dtype)
        return empty, empty

    try:
        xf = torch.fft.fft(x)
        h = torch.zeros(n_samples, device=x.device, dtype=xf.dtype)
        if n_samples % 2 == 0:
            h[0] = h[n_samples // 2] = 1
            h[1 : n_samples // 2] = 2
        else:
            h[0] = 1
            h[1 : (n_samples + 1) // 2] = 2
        xa = torch.fft.ifft(xf * h)
        return xa.abs(), xa.angle()
    except Exception as e:
        if x.device.type != "cuda":
            raise RuntimeError(f"Hilbert変換に失敗いたしましたわ: {e}") from e

        # cuFFT の制限やメモリ不足が発生した場合、CPUへ安全にフォールバックしますの
        x_cpu = x.cpu()
        xf_cpu = torch.fft.fft(x_cpu)
        h_cpu = torch.zeros(n_samples, device=x_cpu.device, dtype=xf_cpu.dtype)
        if n_samples % 2 == 0:
            h_cpu[0] = h_cpu[n_samples // 2] = 1
            h_cpu[1 : n_samples // 2] = 2
        else:
            h_cpu[0] = 1
            h_cpu[1 : (n_samples + 1) // 2] = 2
        xa_cpu = torch.fft.ifft(xf_cpu * h_cpu)
        return xa_cpu.abs().to(x.device), xa_cpu.angle().to(x.device)


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


def extract_tensor_features(
    y: torch.Tensor, sr: int, device: torch.device, spectro_path: str | None = None
) -> dict[str, float]:
    """1つの波形テンソルに対する Spectral Flux, Welch PSD Peak, Subbass Envelope 計測射ですわ！"""
    y = y.to(device)
    features: dict[str, float] = {}

    # 1. Spectral Flux and Welch PSD Peaks
    if spectro_path and os.path.exists(spectro_path):
        stft_mag = torch.from_numpy(np.load(spectro_path, mmap_mode="r")).to(device)
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        psd = stft_mag.pow(2).mean(dim=-1)
        freqs = torch.linspace(0, sr / 2, psd.shape[-1], device=device)
    else:
        window_1024 = torch.hann_window(1024, device=device)
        stft_mag = torch.stft(
            y, n_fft=1024, window=window_1024, return_complex=True
        ).abs()
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        freqs, psd = welch_psd(y, sr=sr)

    features["spectral_flux_mean"] = float(flux.mean().item())
    features["spectral_flux_std"] = float(flux.std().item())

    # 2. Welch PSD Peaks
    peak_idx = psd.argmax()
    features["psd_peak_freq"] = float(freqs[peak_idx].item())
    features["psd_peak_val"] = float(psd[peak_idx].item())

    # 3. Phase Envelope (Sub-bass: 20-60Hz などの帯域別)
    sub_env = fft_bandpass_envelope(y, sr, 20.0, 60.0)
    features["subbass_env_mean"] = float(sub_env.mean().item())

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
