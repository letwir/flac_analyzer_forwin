# Walkthrough: 48.6m Real-File CUDA + CPU Verification (`v2`)

- **Test Track**: `testFLAC\ユビノタクト [2023] 私たちだけの百合の秘蜜は誰も知らない 主観バージョン [Disc 01].flac` Track 4 (`start_sample=289264000`, `end_sample=429264000`, `48kHz`, `48.61m`).
- **Telemetry Before Fix (`task-600`)**: `Total RSS peak = 30,604.6 MB`, `Total VRAM peak = 15,860 MiB (minFree = 138 MiB)`, `GPU tensor = 8.52s`, `CPU 6-stem = 522.99s`.
- **Telemetry After Fix (`task-674`)**: `Total RSS peak = 14,205.9 MB`, `Total VRAM peak = 8,507 MiB (minFree = 7,491 MiB)`, `Post-cleanup VRAM = 1,337 MiB used / 14,661 MiB free`, `Demucs CUDA = 242.01s`, `GPU tensor = 0.99s`, `CPU 6-stem + Essentia = 275.65s`, `Essentia keys = 453`, `vocal_profile` (13 keys), `bass_profile` (14 keys).
- **Regression Suite**: Python `unittest` (`24 + 9 = 33/33 OK`) and Go (`orchestrator/...` all OK).
