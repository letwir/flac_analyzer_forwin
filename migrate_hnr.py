"""
migrate_hnr.py (Root Forwarder to zig/migrate_hnr.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.migrate_hnr import (
    calc_hnr_db,
    calc_nap_from_hnr_db,
    migrate_record_features,
    migrate_stem_scalars,
    update_flac_hnr_tags,
    main,
)

if __name__ == "__main__":
    main()
