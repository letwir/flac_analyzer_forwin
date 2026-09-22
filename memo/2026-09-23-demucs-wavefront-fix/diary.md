# Diary: Demucs wavefront cancellation test fix

- The first bounded `agy` run reported success without producing a real diff; main-agent inspection caught the mismatch.
- A correction run produced the intended one-file diff. Main-agent diff inspection and focused testing confirmed it.
- Full native testing remains environment-limited by VirtualLock quota and worker-daemon handshake behavior.
