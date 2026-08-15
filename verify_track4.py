"""
verify_track4.py (Root Forwarder to zig/verify_track4.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.verify_track4 import (
    run_verification,
    main,
)

if __name__ == "__main__":
    main()
