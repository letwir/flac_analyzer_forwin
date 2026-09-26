# Task Report - agy GPT-6-Luna fallback

## Result

Updated the active harness routing rules so agy remains the preferred bounded CODE route. If the executable or CLI cannot launch after one bounded retry, the main GPT-6-Luna agent may continue the same task. Authentication, approval, policy, and backend failures after successful launch still stop and report. Partial workspace changes must be inspected before fallback, and concurrent writers are prohibited.

## Files

- `C:\Users\letwir\.harness\rules\agy.lrf`
- `C:\Users\letwir\.harness\rules\engineering.lrf`
- `C:\Users\letwir\.harness\rules\MANUAL.lrf`
- `C:\Users\letwir\.harness\skills\agy-subagent-router\SKILL.md`

## Verification

- Strict harness-lint passed for agy.lrf, engineering.lrf, MANUAL.lrf, and agy-subagent-router/SKILL.md (the Markdown scan found zero LRF targets).
- `invoke-rule-preflight.ps1 -Task change -Tag code,agy` passed with 7 files and 79 selected rules.
- `git diff --check` passed for the tracked target files after normalizing the inserted LRF line ending.
- agy model calls did not return a usable response; this task changed routing policy only and did not retry the earlier software implementation.

## Limits

The new fallback applies to launch failure only. A provider/backend failure after agy has successfully launched remains a reported failure, per the requested boundary.
