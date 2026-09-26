# Knowledge - agy GPT-6-Luna fallback - 2026-09-23

## Context

The user requested that CODE work fall back to GPT-6-Luna when agy cannot start. The active harness had mandatory agy routing and no main-agent fallback.

## Verified changes

- `rules/agy.lrf` now defines `agy.fallback`: one bounded launch retry, then the main GPT-6-Luna agent may execute the same bounded task.
- `rules/engineering.lrf`, `rules/MANUAL.lrf`, and the agy router skill point to this behavior.
- Authentication, policy, approval, and backend failures after a successful launch do not trigger fallback. A failed launched editor must be stopped and partial workspace changes inspected before continuing.
- The harness has many pre-existing dirty and untracked files. This task used surgical text replacements and did not stage or commit anything.

## Verification and uncertainty

Strict harness-lint passed for the three LRF targets; the Markdown scan found zero LRF targets. The active rule preflight passed. No new session was started to verify instruction loading. The earlier agy calls started but did not return a usable model response, so the launch-only fallback was not exercised.
