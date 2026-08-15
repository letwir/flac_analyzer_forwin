"""
update_hardware_specs.py (Root Forwarder to zig/update_hardware_specs.py)
"""
import sys
import os

PROJECT_ROOT = os.path.abspath(os.path.dirname(__file__))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from zig.update_hardware_specs import (
    get_sys_info,
    update_hardware_specs_file,
)

if __name__ == "__main__":
    specs = get_sys_info()
    if specs:
        update_hardware_specs_file(specs)
    else:
        print("Failed to auto-detect hardware specs.")
