"""
inspect_track.py (Root Forwarder to zig/inspect_track.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.inspect_track import (
    inspect_flac,
    main,
)

if __name__ == "__main__":
    main()
