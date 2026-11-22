# Diary: 48.6m Long-Track Stress Hardening (`2026-11-23`)

- Executed real-file stress testing on `testFLAC` 48.6-minute track (`task-600`).
- Initial run completed cleanly after `structure_ssm.py` OOM fix, but crossed both monitoring thresholds (`15,860 MiB` VRAM, `30,604.6 MB` RSS).
- Inspected `analyzer/tensor_dsp.py` and found full-track `2^28` cuFFT in `calc_hnr_nap_tensor` + CUDA OOM CPU fallbacks in `fft_bandpass_envelope`/`hilbert_envelope_phase`, plus stereo stem retention in `models.py` and un-bounded YIN/Hilbert/Tempogram/`_power`/chroma loops.
- Bounded and vectorized all bottlenecks cleanly (`task-674`), cutting peak VRAM nearly in half (`8,507 MiB`, `7,491 MiB` free) and peak RSS by >50% (`14,205.9 MB`).
