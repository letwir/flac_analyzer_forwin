# Implementation Plan: Gatekeeper Effective Available RAM Model Refactoring

- **Goal**: Go オーケストレーターの Gatekeeper (GO/NOGO 判定) ロジックを、他アプリのメモリ消費を巻き込んで無用なディスパッチ停止を起こしていた旧来の `(Used + InFlight + Task) > MaxUsable (MaxRamRatio)` から、実質物理空きRAM ($R_{\text{avail}} - R_{\text{inFlight}} \ge R_{\text{task}} + R_{\text{min}}$) モデルへ完全リファクタリングする。
- **Target**: `orchestrator/dispatcher/dispatcher.go`, `docs/cpu_parallelism_and_ram_guard.md`.
- **Feature**:
  - `dispatcher.go`: `effectiveAvailBytes := memInfo.AvailPhys - inFlight` による直感的な実質空き物理RAM判定へ変更。`MemoryLoad >= 90%` 緊急ガードを統合。
  - `docs/cpu_parallelism_and_ram_guard.md`: 実質空きRAMモデルの数式と挙動記述へ更新。
- **Status**: Completed
