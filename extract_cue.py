import sys
import json
import dataclasses
from flac_decode import build_flac_handle

def main():
    if len(sys.argv) < 2:
        print("Usage: extract_cue.py <flac_path>")
        sys.exit(1)
        
    flac_path = sys.argv[1]
    
    try:
        handle = build_flac_handle(flac_path)
        slices = [dataclasses.asdict(s) for s in handle.slices]
        print(json.dumps({
            "status": "success",
            "slices": slices,
            "tags": handle.tags,
            "sample_rate": handle.sample_rate,
            "total_samples": handle.total_samples
        }))
    except Exception as e:
        print(json.dumps({
            "status": "error",
            "message": str(e)
        }))
        sys.exit(1)

if __name__ == "__main__":
    main()
