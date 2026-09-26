# Walkthrough - Demucs RAM admission implementation - 2026-09-26-demucs-ram-admission-implementation

## 概要

Feederで後段track ticketを先に確保し、worker配布直前にOS RAMとGPU観測を再確認する実装を行った。ticketはstem wavefront、GPU cleanup、SHMとcacheのcleanupが完了するまで保持する。

## 成果物一覧

- `docs/demucs_ram_admission_requirements.lrf`
- `orchestrator/dispatcher/admission.go`, `durable_queue.go`, `pipeline_demucs.go`, `pipeline_features.go`, `pipeline_step.go`, `single_task.go`
- `orchestrator/dispatcher/gatekeeper.go`, `shm_windows.go`, `storage_defense.go` と関連テスト
- `orchestrator/planner/resource_plan.go` と関連テスト

既存DBスキーマ/レーンとexclusive `-single-file` の意味は変更せず、commit/push/live writeも行っていない。

## 検証結果

- `go test ./...`（`orchestrator/`）成功。
- `git diff --check` 成功。
- 要件LRFのstrict lint成功。
- agyの初回実装試行はタイムアウトし、作業記録の誤ったCompleted記述を訂正した。今回の結果はその試行ではなく、ユーザー許可後のmainによる継続と検証に基づく。

残余リスク: 実機GPU/SHMでの負荷試験は未実施。OS/GPUドライバの挙動、外部プロセス、資源推定誤差によるOOMは排除できない。
