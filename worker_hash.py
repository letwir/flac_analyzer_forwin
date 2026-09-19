import argparse
import json
import re
import sys
import time

from flac_decode import build_flac_handle, process_slice_with_seq_safety

_HEX32_RE = re.compile(r"^[0-9a-f]{32}$")


def main():
    parser = argparse.ArgumentParser(description="Standalone CPU hash CLI for FLAC files")
    parser.add_argument("--flac-path", required=True, help="Path to FLAC file")
    parser.add_argument("--start-sample", type=int, default=0, help="Start sample index")
    parser.add_argument("--end-sample", type=int, default=-1, help="End sample index")
    args = parser.parse_args()

    flac_path = args.flac_path
    start_sample = args.start_sample
    end_sample = args.end_sample

    try:
        t_dec_start = time.perf_counter()
        handle = build_flac_handle(flac_path)
        start_samp = start_sample
        end_samp = end_sample if end_sample != -1 else handle.total_samples

        _, md5_hash = process_slice_with_seq_safety(
            flac_path,
            start_samp,
            end_samp,
            handle.sample_rate,
            handle.channels
        )
        decode_dur = time.perf_counter() - t_dec_start

        if not md5_hash or not isinstance(md5_hash, str) or len(md5_hash) == 0:
            raise ValueError(f"Empty or invalid hash returned: {md5_hash}")

        # Validate 32 lowercase hex characters (MD5)
        normalized = md5_hash.strip().lower()
        if not _HEX32_RE.match(normalized):
            raise ValueError(
                f"Hash is not valid 32-char lowercase hex: {md5_hash!r}"
            )

        result = {
            "status": "success",
            "audio_hash": normalized,
            "profile": {
                "decode": decode_dur
            }
        }
        print(json.dumps(result), flush=True)
        sys.exit(0)
    except Exception as e:
        # Write error details to stderr so the Go runner exposes meaningful failure
        print(f"worker_hash error: {e}", file=sys.stderr, flush=True)
        error_result = {
            "status": "error",
            "message": str(e)
        }
        print(json.dumps(error_result), flush=True)
        sys.exit(1)

if __name__ == "__main__":
    main()
