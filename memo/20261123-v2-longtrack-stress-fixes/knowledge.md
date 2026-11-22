# Knowledge: v2 48.6-Minute Long-Track Memory/VRAM Hardening

- **Root Cause of `15,860 MiB` VRAM & `30,604.6 MB` RSS Spike on 48.6m Track**:
  - `analyzer/tensor_dsp.py`: `calc_hnr_nap_tensor` zero-padded full track (`1.286e8` samples @ 44.1kHz) to `2^28` (`268,435,456` points) cuFFT despite searching only lags `sr/500 .. sr/50`. `fft_bandpass_envelope` & `hilbert_envelope_phase` ran full-track cuFFT, hit VRAM OOM at `15,860 MiB`, and fell back to multi-GB CPU allocations (`30,604.6 MB` RSS).
  - Bounded `_bound_tensor_windows` (`max_samples=1,048,576`, `win_len=65,536`) and chunked `16 x 65,536` (`n_fft=131,072`) PSD averaging in `calc_hnr_nap_tensor` reduced `extract_tensor_features` from `8.52s` to `0.99s` and dropped peak VRAM from `15,860 MiB` (`138 MiB` free) to `8,507 MiB` (`7,491 MiB` free).
- **CPU/RAM Vectorization & Incremental Release**:
  - `models.py` (`HTDemucsSeparator.separate`): pops and frees each stereo stem immediately after `stereo_field_morphism` + `mono_downmix_morphism`, eliminating `7.2 GB` peak overlap.
  - `analyzer/stats.py` (`_calc_scipy_stats_features`, `_calc_peak_features`, `_calc_hilbert_features`), `analyzer/librosa_tonal.py` (`extract_chroma_obj`), `analyzer/core.py` (`chroma_cqt`, `tempobeat`, `tempogram`, clearing `self._power` after `mel`), `analyzer/essentia_dsp.py` (`_calc_vocal_f0_seq`), and `analyzer/common_pitch_harmony.py` (`voiced_f0_contour_morphism_Large`) bound long-track arrays.
  - Combined fixes reduced 48.6-minute track peak RSS from `30,604.6 MB` to `14,205.9 MB` and CPU 6-stem time from `522.99s` to `275.65s`.
