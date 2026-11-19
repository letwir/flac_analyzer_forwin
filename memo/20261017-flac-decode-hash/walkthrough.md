# Walkthrough: 20261017-flac-decode-hash

## Overview

Repair missing short-decoder fallback and integer/float hash drift in flac_decode.py. Plan audited with native-byte parity, short-read rejection, seek fallback and process-lifecycle requirements; independent final review PASS.

## Deliverables

- flac_decode.py: narrow missing-executable fallback; native PCM16/24/32 bytes; canonical long fallback; child/pipes cleanup before fallback and after completion.
- test_flac_decode.py: 11 focused regression tests at repository root because tests/ is ignored by this checkout.
- Task evidence under memo/20261017-flac-decode-hash. No Go source, EXE, configuration, dependency, production music, DB rows or Git index/history changed.

## Verification

- Before fix, isolated reproduction: short missing CLI unhandled, long hash differed from native integer PCM, PCM24 fallback emitted 32-bit data.
- `.venv\Scripts\python.exe -m unittest -v test_flac_decode`: 11 tests passed, exit 0.
- Actual `worker_hash.py` subprocess on synthetic PCM24 stereo slice: expected MD5 matched before and after mutagen tag mutation, exit 0.
- Independent in-memory stereo PCM24 extrema/open-ended slice exact bytes and MD5: PASS, exit 0.
- Independent failed-stream ordering: both pipes closed, child terminated/waited before fallback; PASS, exit 0.
- `git diff --check -- flac_decode.py test_flac_decode.py`: exit 0.
- Go guard regression check is recorded in receipts.json when complete.

Residual: real flac CLI unavailable, so direct installed-CLI comparison and production batch completion are unverified. PCM32 byte coverage uses WAV fixture. Existing inconsistent DB hash records were not migrated. Prompt defect: none supported. Agent/tool friction and corrections are recorded in diary.md. Date: 2026-10-17. Tags: flac, PCM, MD5, local-validation.
