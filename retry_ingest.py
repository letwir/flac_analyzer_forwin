"""
retry_ingest.py (Root Forwarder to zig/retry_ingest.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.retry_ingest import (
    get_db_url,
    main,
)

if __name__ == "__main__":
    main()
