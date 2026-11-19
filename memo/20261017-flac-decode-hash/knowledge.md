# Knowledge Base: 20261017-flac-decode-hash

<api id="flac-decode-native-pcm">

## Context

Verified 2026-10-17 in the Windows checkout. User logs showed a Track 6 source-hash mismatch and a Track 7 missing decoder executable. No live PostgreSQL lookup or production audio modification was performed.

## Findings

`flac.exe` was absent from the inspected PATH. The short decoder did not catch the resulting FileNotFoundError. The long decoder instead fell back to float32 data and hashed those bytes, unlike the CLI path's integer PCM. The short soundfile fallback also expanded PCM24 into 32-bit bytes. Synthetic FLAC fixtures reproduced all three failures before editing.

Fallback now returns native little-endian PCM16/24/32 (packed 24-bit bytes), and long fallback uses those same bytes for both waveform conversion and MD5. Unsupported formats and short reads fail explicitly. Missing executable handling is narrow; libsndfile seek failures retain sequential fallback. Failed children and both pipes are cleaned up before fallback with bounded waits. The Go source-hash and row-version guards were preserved.

## Morphism

FLAC slice -> native raw integer PCM -> MD5 identity and float waveform. Metadata edits do not change the raw PCM identity. This restores method.md's existing hash contract; it supersedes the inconsistent float32 fallback, not a schema or DB migration.

Tags: flac, decoder, PCM24, audio-hash, fallback. Plan: bounded decoder-only repair with isolated regression checks. Production row identity remains unverified; existing inconsistent stored hashes were not rewritten.

</api>
