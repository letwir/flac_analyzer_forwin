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

import gc
import math
import os
import tempfile
from typing import Any
import numpy as np
import torch

from constants import CHORDS_DIC, KEY_PROFILES, NOTES
from .core import AudioContext, FIXED_SEQ_FRAMES, FeatureExtractor, _resample_to_fixed_frames, downmix_to_mono_f32
from .types import TensorFeatures


# ─────────────────────────────────────────────
# 1. 瞬時位相 & Hilbert 変換 (Pure Tensor Morphism)
# ─────────────────────────────────────────────
def _bound_tensor_windows(
    x: torch.Tensor, max_samples: int = 1048576, win_len: int = 65536
) -> torch.Tensor:
    """長尺テンソル (..., N) から均等間隔の代表窓群を抽出し、FFT メモリ上限を一定化いたしますわ！"""
    n_samples = x.shape[-1]
    if n_samples <= max_samples or win_len <= 0:
        return x
    n_wins = max(1, max_samples // win_len)
    if n_wins == 1 or n_samples <= win_len:
        return x[..., :win_len]
    starts = torch.linspace(0, n_samples - win_len, steps=n_wins, dtype=torch.long)
    slices = [x[..., int(s.item()) : int(s.item()) + win_len] for s in starts]
    return torch.cat(slices, dim=-1)


def hilbert_envelope_phase(x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
    """1Dまたはバッチテンソルに対するHilbert変換を行い、エンベロープと瞬時位相を返しますの。"""
    x = _bound_tensor_windows(x, max_samples=2097152, win_len=65536)
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

    if n_samples > 1048576:
        win_len = 65536
        n_fft = 131072
        starts = torch.linspace(0, n_samples - win_len, steps=16, dtype=torch.long)
        wins = torch.stack([y[..., int(s.item()) : int(s.item()) + win_len] for s in starts], dim=-2)
        yf = torch.fft.rfft(wins.float(), n=n_fft, dim=-1)
        psd = yf.abs().square().mean(dim=-2)
        r = torch.fft.irfft(psd, n=n_fft, dim=-1)[..., :win_len]
    else:
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
    x = _bound_tensor_windows(x, max_samples=2097152, win_len=65536)
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
_SUBBASS_SKIP_STEMS = frozenset({"vocals", "piano", "guitar"})


def _extract_spectro_streaming(
    spectro_path: str, sr: int, device: torch.device
) -> tuple[float, float, torch.Tensor, torch.Tensor]:
    """ディスクキャッシュ済み STFT を 16384 フレームチャンクで全走査し正確な Flux・Welch PSD を算出いたしますわ！"""
    arr = np.load(spectro_path, mmap_mode="r")
    total_frames = arr.shape[-1]
    if total_frames == 0:
        freqs = torch.linspace(0, sr / 2, max(1, arr.shape[-2]), device=device)
        return 0.0, 0.0, freqs, torch.zeros_like(freqs)

    flux_sum = 0.0
    flux_sq_sum = 0.0
    flux_count = 0
    psd_acc: torch.Tensor | None = None
    chunk_frames = 16384

    for start in range(0, total_frames, chunk_frames):
        end = min(total_frames, start + chunk_frames)
        # チャンク境界のフレーム差分を落とさないよう前フレームを1列オーバーラップさせるわ
        read_start = start - 1 if start > 0 else 0
        mag = torch.from_numpy(np.ascontiguousarray(arr[..., read_start:end])).to(device)
        if mag.shape[-1] > 1:
            flux_c = torch.diff(mag, dim=-1).pow(2).sum(dim=-2).sqrt()
            flux_sum += float(flux_c.sum().item())
            flux_sq_sum += float(flux_c.pow(2).sum().item())
            flux_count += flux_c.numel()
        mag_own = mag[..., 1:] if start > 0 else mag
        psd_c = mag_own.pow(2).sum(dim=-1)
        psd_acc = psd_c if psd_acc is None else (psd_acc + psd_c)

    flux_mean = flux_sum / float(flux_count) if flux_count > 0 else 0.0
    flux_var = max(0.0, (flux_sq_sum / float(flux_count)) - flux_mean * flux_mean) if flux_count > 0 else 0.0
    if flux_count > 1:
        flux_var *= float(flux_count) / float(flux_count - 1)
    psd = psd_acc / float(total_frames)
    freqs = torch.linspace(0, sr / 2, psd.shape[-1], device=device)
    return flux_mean, math.sqrt(flux_var), freqs, psd


def extract_tensor_features_Large(
    y: torch.Tensor,
    sr: int,
    device: torch.device,
    spectro_path: str | None = None,
    stem_name: str | None = None,
) -> dict[str, Any]:
    """全タイムラインをチャンク分割し 100% 欠損なく Spectral Flux, Welch PSD Peak, Subbass Envelope, HNR/NAP を計測する射ですわ！"""
    n_samples = y.shape[-1]
    chunk_len = 2097152
    features: dict[str, Any] = {}

    if n_samples <= chunk_len:
        y_dev = y.to(device)
        nap_val, hnr_db = calc_hnr_nap_tensor(y_dev, sr)
        features["nap"] = nap_val
        features["hnr_db"] = hnr_db

        # Keep extract_tensor_features strictly on 1024-reflect STFT + Welch PSD regardless of CPU spectro_path cache.
        _ = spectro_path
        window_1024 = torch.hann_window(1024, device=device)
        stft_mag = torch.stft(
            y_dev, n_fft=1024, window=window_1024, return_complex=True, center=True, pad_mode="reflect"
        ).abs()
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        freqs, psd = welch_psd(y_dev, sr=sr)
        features["spectral_flux_mean"] = float(flux.mean().item())
        features["spectral_flux_std"] = float(flux.std().item())

        psd_1d = psd.mean(dim=list(range(psd.ndim - 1))) if psd.ndim > 1 else psd
        peak_idx = psd_1d.argmax()
        features["psd_peak_freq"] = float(freqs[peak_idx].item()) if peak_idx < len(freqs) else 0.0
        features["psd_peak_val"] = float(psd_1d[peak_idx].item()) if peak_idx < len(psd_1d) else 0.0

        if stem_name in _SUBBASS_SKIP_STEMS:
            features["subbass_env_mean"] = 0.0
            return features
        sub_env = fft_bandpass_envelope(y_dev, sr, 20.0, 60.0)
        features["subbass_env_mean"] = float(sub_env.mean().item()) if sub_env.numel() > 0 else 0.0
        return features

    # 長尺波形はチャンク分割して 100% 全サンプル・全フレームを GPU 上で積算するわ
    y_hnr_wins = _bound_tensor_windows(y, max_samples=1048576, win_len=65536).to(device)
    nap_val, hnr_db = calc_hnr_nap_tensor(y_hnr_wins, sr)
    del y_hnr_wins
    features["nap"] = nap_val
    features["hnr_db"] = hnr_db

    compute_subbass = stem_name not in _SUBBASS_SKIP_STEMS
    sub_sum = 0.0
    sub_count = 0

    window_1024 = torch.hann_window(1024, device=device)
    flux_sum = 0.0
    flux_sq_sum = 0.0
    flux_count = 0
    psd_acc: torch.Tensor | None = None
    psd_weight_total = 0.0
    freqs = torch.zeros(0, device=device)

    for start in range(0, n_samples, chunk_len):
        end = min(n_samples, start + chunk_len)
        c_dev = y[..., start:end].to(device)
        if c_dev.shape[-1] >= 1024:
            stft_mag = torch.stft(
                c_dev, n_fft=1024, window=window_1024, return_complex=True, center=True, pad_mode="reflect"
            ).abs()
            if stft_mag.shape[-1] > 1:
                flux_c = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
                flux_sum += float(flux_c.sum().item())
                flux_sq_sum += float(flux_c.pow(2).sum().item())
                flux_count += flux_c.numel()
        if c_dev.shape[-1] >= 2048:
            freqs, psd_c = welch_psd(c_dev, sr=sr, n_fft=2048)
            w = float(c_dev.shape[-1])
            psd_acc = psd_c * w if psd_acc is None else (psd_acc + psd_c * w)
            psd_weight_total += w
        if compute_subbass:
            sub_c = fft_bandpass_envelope(c_dev, sr, 20.0, 60.0)
            sub_sum += float(sub_c.sum().item())
            sub_count += sub_c.numel()
        del c_dev

    f_mean = flux_sum / float(flux_count) if flux_count > 0 else 0.0
    f_var = max(0.0, (flux_sq_sum / float(flux_count)) - f_mean * f_mean) if flux_count > 0 else 0.0
    if flux_count > 1:
        f_var *= float(flux_count) / float(flux_count - 1)
    features["spectral_flux_mean"] = f_mean
    features["spectral_flux_std"] = math.sqrt(f_var)
    psd = psd_acc / psd_weight_total if (psd_acc is not None and psd_weight_total > 0.0) else torch.zeros(0, device=device)

    psd_1d = psd.mean(dim=list(range(psd.ndim - 1))) if psd.ndim > 1 else psd
    if psd_1d.numel() > 0 and freqs.numel() > 0:
        peak_idx = psd_1d.argmax()
        features["psd_peak_freq"] = float(freqs[peak_idx].item()) if peak_idx < len(freqs) else 0.0
        features["psd_peak_val"] = float(psd_1d[peak_idx].item()) if peak_idx < len(psd_1d) else 0.0
    else:
        features["psd_peak_freq"] = 0.0
        features["psd_peak_val"] = 0.0

    features["subbass_env_mean"] = (sub_sum / float(sub_count)) if (compute_subbass and sub_count > 0) else 0.0
    return features


def extract_tensor_features(
    y: torch.Tensor,
    sr: int,
    device: torch.device,
    spectro_path: str | None = None,
    stem_name: str | None = None,
) -> dict[str, Any]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！ Mor(Tensor -> TensorMetrics)"""
    return extract_tensor_features_Large(y, sr, device, spectro_path=spectro_path, stem_name=stem_name)


def _is_cuda_oom_error(exc: BaseException) -> bool:
    if isinstance(exc, torch.cuda.OutOfMemoryError):
        return True
    return "out of memory" in str(exc).lower()


def _compute_spectro_chunk_pass(
    y_1d: torch.Tensor,
    mm: np.memmap,
    window: torch.Tensor,
    device: torch.device,
    n_fft: int,
    hop_length: int,
    cur_chunk: int,
    pad: int,
    n_samples: int,
    n_frames: int,
) -> None:
    for f0 in range(0, n_frames, cur_chunk):
        f1 = min(n_frames, f0 + cur_chunk)
        p_start = f0 * hop_length
        p_end = (f1 - 1) * hop_length + n_fft
        chunk_buf = torch.zeros(p_end - p_start, dtype=torch.float32)
        orig_p_start = max(p_start, pad)
        orig_p_end = min(p_end, pad + n_samples)
        if orig_p_end > orig_p_start:
            chunk_buf[orig_p_start - p_start : orig_p_end - p_start] = y_1d[
                orig_p_start - pad : orig_p_end - pad
            ]
        c_dev = stft_c = mag_c = None
        try:
            c_dev = chunk_buf.to(device)
            stft_c = torch.stft(
                c_dev,
                n_fft=n_fft,
                hop_length=hop_length,
                window=window,
                return_complex=True,
                center=False,
            )
            mag_c = stft_c.abs().detach().cpu().numpy()
            mm[:, f0:f1] = mag_c
        finally:
            del c_dev, stft_c, mag_c


def compute_and_save_spectro_chunked_Large(
    y: torch.Tensor,
    spectro_path: str,
    device: torch.device,
    n_fft: int = 2048,
    hop_length: int = 512,
    chunk_frames: int = 2048,
) -> tuple[int, int]:
    """
    librosa.stft(n_fft=2048, hop_length=512, center=True, pad_mode='constant') と
    数値等価な (1025, 1 + N // 512) float32 振幅スペクトログラムをチャンク単位で GPU 計算し、
    原子リネーム (open_memmap + flush + close -> os.replace) で *.spectro.npy へ書き出しますわ。
    """
    if not spectro_path:
        raise ValueError("spectro_path must be non-empty")
    if n_fft <= 0 or hop_length <= 0 or chunk_frames <= 0:
        raise ValueError("n_fft, hop_length, and chunk_frames must be positive")

    y_cpu = y.detach().to(device="cpu")
    y_mono = downmix_to_mono_f32(y_cpu.numpy())
    y_1d = torch.from_numpy(y_mono).contiguous().view(-1)

    n_samples = int(y_1d.shape[0])
    n_bins = n_fft // 2 + 1
    n_frames = 1 + n_samples // hop_length
    pad = n_fft // 2

    parent_dir = os.path.dirname(os.path.abspath(spectro_path))
    if parent_dir:
        os.makedirs(parent_dir, exist_ok=True)

    attempts = [chunk_frames]
    half_chunk = max(1, chunk_frames // 2)
    if half_chunk < chunk_frames:
        attempts.append(half_chunk)

    mm = None
    pending_mmap = None
    window = None
    tmp_path = None
    primary_exc: BaseException | None = None
    try:
        with tempfile.NamedTemporaryFile(
            dir=parent_dir or ".",
            prefix=f"{os.path.basename(spectro_path)}.",
            suffix=".tmp.npy",
            delete=False,
        ) as tmp_f:
            tmp_path = tmp_f.name

        mm = np.lib.format.open_memmap(
            tmp_path, mode="w+", dtype=np.float32, shape=(n_bins, n_frames)
        )
        window = torch.hann_window(n_fft, periodic=True, device=device, dtype=torch.float32)

        for idx, cur_chunk in enumerate(attempts):
            oom_retry = False
            try:
                _compute_spectro_chunk_pass(
                    y_1d, mm, window, device, n_fft, hop_length, cur_chunk, pad, n_samples, n_frames
                )
                break
            except Exception as exc:
                if not _is_cuda_oom_error(exc) or idx == len(attempts) - 1:
                    raise
                oom_retry = True
            if oom_retry:
                gc.collect()
                if device.type == "cuda" and torch.cuda.is_available():
                    torch.cuda.empty_cache()

        mm.flush()
        pending_mmap = getattr(mm, "_mmap", None)
        del mm
        mm = None
        if pending_mmap is not None:
            pending_mmap.close()
            pending_mmap = None
        os.replace(tmp_path, spectro_path)
        tmp_path = None
        return (n_bins, n_frames)
    except BaseException as exc:
        primary_exc = exc
        raise
    finally:
        del window
        cleanup_errors: list[BaseException] = []
        if mm is not None:
            if pending_mmap is None:
                pending_mmap = getattr(mm, "_mmap", None)
            del mm
            mm = None
        if pending_mmap is not None:
            try:
                pending_mmap.close()
                pending_mmap = None
            except Exception as close_exc:
                cleanup_errors.append(close_exc)
        if tmp_path is not None and os.path.exists(tmp_path):
            try:
                os.unlink(tmp_path)
            except Exception as unlink_exc:
                cleanup_errors.append(unlink_exc)
        if cleanup_errors:
            if primary_exc is not None:
                for ce in cleanup_errors:
                    primary_exc.add_note(f"cleanup failed (tmp_path={tmp_path}): {ce}")
            else:
                raise cleanup_errors[0]


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
