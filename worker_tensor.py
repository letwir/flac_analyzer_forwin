"""
worker_tensor.py
================
Goから起動される PyTorch (tensor) 依存のワーカーですわ。
CUDA 13 / CPU フォールバックによる高速なFFT・テンソル計算を担当し、
瞬時位相(Phase)、PSD、Spectral Flux、帯域別Envelope 等を抽出しますの。
"""

import argparse
import json
import logging
import os
import sys
import time
import torch

# プロジェクト内のモジュール
import shm_interop

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(levelname)s] [%(name)s] %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def hilbert_envelope_phase(x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
    """1Dテンソルに対するHilbert変換を行い、エンベロープと瞬時位相を返しますの。"""
    N = x.shape[-1]
    try:
        Xf = torch.fft.fft(x)
        h = torch.zeros(N, device=x.device, dtype=Xf.dtype)
        if N % 2 == 0:
            h[0] = h[N//2] = 1
            h[1:N//2] = 2
        else:
            h[0] = 1
            h[1:(N+1)//2] = 2
        xa = torch.fft.ifft(Xf * h)
        return xa.abs(), xa.angle()
    except Exception as e:
        if x.device.type == "cuda":
            # cuFFT の制限やメモリ不足が発生した場合、CPUへ安全にフォールバックしますの
            x_cpu = x.cpu()
            Xf = torch.fft.fft(x_cpu)
            h = torch.zeros(N, device=x_cpu.device, dtype=Xf.dtype)
            if N % 2 == 0:
                h[0] = h[N//2] = 1
                h[1:N//2] = 2
            else:
                h[0] = 1
                h[1:(N+1)//2] = 2
            xa = torch.fft.ifft(Xf * h)
            return xa.abs().to(x.device), xa.angle().to(x.device)
        raise e

def welch_psd(x: torch.Tensor, sr: int, n_fft: int = 2048) -> tuple[torch.Tensor, torch.Tensor]:
    """Welch法に近い平均化PSDを求めますわ。"""
    window = torch.hann_window(n_fft, device=x.device)
    # stft takes (..., N) and returns (..., F, T)
    stft = torch.stft(x, n_fft=n_fft, window=window, return_complex=True, 
                      hop_length=n_fft//2, center=False)
    psd = stft.abs().pow(2).mean(dim=-1)
    freqs = torch.linspace(0, sr / 2, psd.shape[-1], device=x.device)
    return freqs, psd

def fft_bandpass_envelope(x: torch.Tensor, sr: int, f_lo: float, f_hi: float) -> torch.Tensor:
    """FFTベースの理想バンドパスフィルタリング後のエンベロープ抽出ですわ。"""
    try:
        Xf = torch.fft.rfft(x)
        freqs = torch.fft.rfftfreq(x.shape[-1], d=1/sr).to(x.device)
        mask = (freqs >= f_lo) & (freqs <= f_hi)
        Xf_filtered = Xf * mask
        x_filtered = torch.fft.irfft(Xf_filtered, n=x.shape[-1])
        env, _ = hilbert_envelope_phase(x_filtered)
        return env
    except Exception as e:
        if x.device.type == "cuda":
            x_cpu = x.cpu()
            Xf = torch.fft.rfft(x_cpu)
            freqs = torch.fft.rfftfreq(x_cpu.shape[-1], d=1/sr)
            mask = (freqs >= f_lo) & (freqs <= f_hi)
            Xf_filtered = Xf * mask
            x_filtered = torch.fft.irfft(Xf_filtered, n=x_cpu.shape[-1])
            env, _ = hilbert_envelope_phase(x_filtered)
            return env.to(x.device)
        raise e

def extract_tensor_features(y: torch.Tensor, sr: int, device: torch.device, spectro_path: str = None) -> dict:
    y = y.to(device)
    features = {}
    
    # 1. Spectral Flux and Welch PSD Peaks
    if spectro_path and os.path.exists(spectro_path):
        import numpy as np
        # spectro is shape (F, T) from librosa.stft(n_fft=2048)
        stft_mag = torch.from_numpy(np.load(spectro_path, mmap_mode='r')).to(device)
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        psd = stft_mag.pow(2).mean(dim=-1)
        freqs = torch.linspace(0, sr / 2, psd.shape[-1], device=device)
    else:
        stft_mag = torch.stft(y, n_fft=1024, return_complex=True).abs()
        flux = torch.diff(stft_mag, dim=-1).pow(2).sum(dim=-2).sqrt()
        freqs, psd = welch_psd(y, sr=sr)
        
    features["spectral_flux_mean"] = flux.mean().item()
    features["spectral_flux_std"] = flux.std().item()

    # 2. Welch PSD Peaks
    peak_idx = psd.argmax()
    features["psd_peak_freq"] = freqs[peak_idx].item()
    features["psd_peak_val"] = psd[peak_idx].item()

    # 3. Phase Envelope (Sub-bass: 20-60Hz などの帯域別)
    sub_env = fft_bandpass_envelope(y, sr, 20.0, 60.0)
    features["subbass_env_mean"] = sub_env.mean().item()

    return features

def main():
    setup_logger()
    logger = logging.getLogger("TensorWorker")

    parser = argparse.ArgumentParser()
    parser.add_argument("--shm-metadata", required=True, help="JSON string from DemucsWorker")
    parser.add_argument("--track-hash", required=True)
    args = parser.parse_args()

    # CPU/GPU 判定
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    logger.info(f"Using device: {device}")

    try:
        metadata = json.loads(args.shm_metadata)
        sr = metadata["sr"]
        stems_info = metadata["stems"]
    except Exception as e:
        logger.exception("Failed to parse metadata")
        sys.exit(1)

    t_start = time.perf_counter()
    extracted_features = {}

    for stem_name, info in stems_info.items():
        tag_name = info["shm_tag"]
        shape = tuple(info["shape"])
        dtype_name = info["dtype"]
        spectro_path = info.get("spectro_path")
        
        logger.info(f"Processing SHM '{tag_name}' for stem: {stem_name}")
        shm, y_np = shm_interop.attach_shm_read_only(tag_name, shape, dtype_name)
        
        try:
            import warnings
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", UserWarning)
                # torch.from_numpy は Zero-copy でメモリをマッピングしますの
                y_tensor = torch.from_numpy(y_np)
            
            # 特徴量抽出
            stem_feats = extract_tensor_features(y_tensor, sr, device, spectro_path=spectro_path)
            extracted_features[stem_name] = stem_feats
            
        except Exception as e:
            logger.exception(f"Error processing {stem_name}")
            sys.exit(1)
        finally:
            shm.close()

    logger.info(f"All extractions completed in {time.perf_counter() - t_start:.4f}s")
    
    final_features = {"demucs": {}}
    for k, v in extracted_features.items():
        if k == "mix":
            final_features["mix"] = v
        else:
            final_features["demucs"][k] = v
            
    # 結果を出力
    print(json.dumps({"status": "success", "features": final_features}))
    sys.exit(0)

if __name__ == "__main__":
    main()
