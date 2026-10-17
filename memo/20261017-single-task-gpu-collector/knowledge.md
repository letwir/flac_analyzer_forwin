# Knowledge: 20261017-single-task-gpu-collector

Single-file execution bypasses Dispatcher.Start. GPU collection was only started by the ordinary dispatcher resource collector, while admission requires fresh shared-GPU observations. Thus the single-file path retried resource admission indefinitely with an uncollected observation.

Fix: start the existing GPU collector with the signal cancellation context after single-file preflight, only for nonempty task lists. Preserve check-only's earlier return and do not start durable queue workers. Display track starts and resource retry causes. Runtime admission remains fail-closed.

Evidence: orchestrator/main.go, dispatcher/single_task.go, dispatcher/dispatcher.go, dispatcher/stats.go, dispatcher/admission.go, sysinfo/gpu_windows.go. Production batch and DB writes are outside this verification. Validation results are recorded in walkthrough.md.
