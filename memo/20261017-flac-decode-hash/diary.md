# Diary: 20261017-flac-decode-hash

### 2026-10-17 Asia/Tokyo

- Task / request-evidence: Continue batch repair from new pasted logs: Track 6 hash refusal after tagging; Track 7 Popen WinError 2.
- Hypothesis: Missing flac CLI exposes inconsistent short/long fallback and PCM24 hash representations.
- Tried / action: Reproduced three failures with synthetic fixtures, audited a bounded plan, delegated decoder implementation, and reviewed actual differences. Independent checks found cleanup occurred too late; worker corrected ordering before final acceptance.
- Rejected: Removing DB conflict guards, rewriting stored hashes, installing a production dependency, or rerunning the live batch.
- Result / completion evidence: 11 focused tests passed, real worker_hash CLI passed sliced PCM24 hash and metadata-invariance checks, independent review passed. See walkthrough for commands and limits.
- Uncertainty: Actual stored PostgreSQL hashes and production audio acceptance were not inspected. PCM32 test uses WAV because installed libsndfile cannot write PCM32 FLAC fixtures.
- Attribution: PromptDefect 0%, AgentDefect 100% of observed tooling/review friction; user supplied the useful failure evidence. Defect class ToolFailure and interim implementation mistakes, not user ambiguity. Ratios concern process friction, not responsibility for earlier source history.
- Search: Current flac_decode.py, method.md, Go ingest guard, registry and llm-memory precedent lookup. Public/EPUB evidence N_A for local incident.
- Correction: Canonical integer PCM across decoders; selective fallback; bounded process cleanup before alternate decode. Sandbox TEMP fixture failure succeeded when rerun outside sandbox; final tests used workspace-local fixtures.
- Impact: Decoding no longer requires flac CLI when soundfile can read the file; equivalent audio keeps equivalent identity across fallback routes.
- Feedback / rewritten-request: Repair missing-decoder and fallback hash drift without weakening source-identity guards; verify with temporary fixtures. No clarification needed.
- Emotion / Thoughts: Keep the actual source of hash identity observable and preserve genuine conflict detection.

Tags: diagnostic, decoder, hash, verification.
