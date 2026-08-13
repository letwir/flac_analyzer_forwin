# Walkthrough: Gatekeeper Effective Available RAM Model Refactoring

- **Summary**: Refactored Gatekeeper evaluation logic to eliminate double-counting and false NOGO delays caused by high external application memory usage.
- **Changes**:
  - `orchestrator/dispatcher/dispatcher.go`: Replaced `(Used + InFlight + Task) > MaxUsable` formula with `EffectiveAvail = AvailPhys - InFlight >= Task + MinAvail`.
  - `docs/cpu_parallelism_and_ram_guard.md`: Updated architecture documentation to reflect the Effective Available RAM model.
- **Verification**: Verified compilation of `orchestrator.exe` and validated logic under high memory load (40GB+ external RAM usage).
