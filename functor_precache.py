"""
functor_precache.py (Root Forwarder to zig/functor_precache.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.functor_precache import (
    main,
)

if __name__ == "__main__":
    main()
