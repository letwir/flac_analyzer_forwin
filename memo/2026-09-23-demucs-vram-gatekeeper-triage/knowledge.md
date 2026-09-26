# Knowledge Base

## Context

Verified from the FLAC Analyzer source on 2026-09-23 while reconciling repeated TaskFeeder admission warnings with `analyzer_queue_length=0`.

## Findings

- `analyzer_queue_length` counts tasks admitted to the in-memory `taskQueue`: it increments only after `d.taskQueue <- task` and decrements when pipeline execution begins.
- A Gatekeeper admission failure takes a different path: `parkTaskForAdmission` stores the task as `FAILED_MAYBE_RETRY` with `next_evaluation_at`, logs the deferral, and increments `analyzer_tasks_total{status="retry_pending"}`. It never increments the in-memory queue gauge.
- After the retry cooldown, `taskFeeder` requeues and claims eligible durable rows, retries admission, and can park the same task again. Thus repeated admission warnings with an empty in-memory queue are consistent.
- The retry-pending metric is a counter, not a current backlog gauge. `analyzer_queue_length=0` does not establish that no durable retry tasks exist.

## Morphism

To expose backlog, add a current gauge for eligible/deferred durable task rows (or rename/document `analyzer_queue_length` as the in-memory admitted queue), while retaining a separate counter for retry attempts. A persistently low VRAM sample can keep a row cycling through deferred status indefinitely; this is an admission liveness issue, not evidence that a worker goroutine is blocked.
