# Walkthrough - agy GPT-6-Luna fallback - 2026-09-23

## Overview

Aligned the active harness rules with the user's requested fallback. agy remains primary. GPT-6-Luna is authorized only if agy is missing or its OS/CLI startup fails before a model request, after one bounded launch retry. The same task scope and permissions carry over; partial workspace changes are inspected before a fallback writer starts.

## Changed files

- `C:\Users\letwir\.harness\rules\agy.lrf`: added `agy.fallback` and linked discovery/intent to it.
- `C:\Users\letwir\.harness\rules\engineering.lrf`: made agy primary with the bounded launch-failure route and maintained the explicit VCS authorization boundary.
- `C:\Users\letwir\.harness\rules\MANUAL.lrf`: aligned the coding role summary.
- `C:\Users\letwir\.harness\skills\agy-subagent-router\SKILL.md`: documented the fallback in the skill description and routing records.

## Checks

- `harness-lint.exe -path <target> -strict`: PASS for agy.lrf, engineering.lrf, and MANUAL.lrf.
- `harness-lint.exe -path <agy router SKILL.md> -strict`: PASS, zero LRF targets discovered.
- `invoke-rule-preflight.ps1 -Task change -Tag code,agy`: PASS; 7 files, 79 selected rules.
- `git diff --check` on the tracked targets: PASS.

## Limits

This task does not alter the Demucs implementation. The earlier agy requests launched but stalled without a usable response; they do not meet the new launch-failure condition. No runtime test of the new fallback was performed.
