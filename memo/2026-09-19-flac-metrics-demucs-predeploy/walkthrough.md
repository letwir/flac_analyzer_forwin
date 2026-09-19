# Walkthrough: FLAC metrics and Demucs recovery pre-deploy

The observed task failures were not caused by verified VRAM exhaustion. The live metrics reported dedicated VRAM availability as valid. The runtime stack instead showed workers blocked while acquiring the single Demucs daemon, including hash checks that did not need the GPU model.

The implementation separates hash calculation into `worker_hash.py`, publishes validity alongside GPU values, reports the Demucs pool registry size, and repairs pool recovery paths after restart or recycle failures. The user subsequently committed the main implementation and used Gemini to remove one unused import and restore a helper required by an existing test.

Local verification completed successfully. The installed binaries were not changed. The runtime host is reachable through Tailscale, but its metrics endpoint currently refuses connections, so deployment, service startup, and live acceptance checks are still required.
