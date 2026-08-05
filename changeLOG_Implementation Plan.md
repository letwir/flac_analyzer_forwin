# Implementation Plan: CUE SHM Optimization & Robustness Fixes

- **Goal**: Fix WinError 1455 by estimating SHM size per task (handling CUE tracks precisely), add DB URL fallback in ingester.py, and suppress scipy moment calculation warnings.

- Target: `run_batch.ps1`
- Feature: Rust高速モード (fd/rg) 自動判定および .NET FileInfo メタデータ取得による処理高速化
- Status: Completed
