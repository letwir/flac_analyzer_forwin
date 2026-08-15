"""
fix_empty_meta.py (Root Forwarder to zig/fix_empty_meta.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.fix_empty_meta import (
    get_db_url,
    extract_vorbis_meta,
    main,
)

if __name__ == "__main__":
    main()
