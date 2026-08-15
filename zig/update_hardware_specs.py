"""
zig/update_hardware_specs.py
============================
PowerShell/CIM を通じてマシンの CPU/RAM/GPU/OS/Pagefile スペックを取得し、
HARDWARE_SPECS.md の <dev_specs id="DEV_SPECS"> ブロックを自動更新する治具スクリプトですわ！

使い方:
    python zig/update_hardware_specs.py
"""

import subprocess
import json
import os
import sys
import re

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

def get_sys_info():
    ps_cmd = """
    $cpu = (Get-CimInstance Win32_Processor | Select-Object -First 1).Name;
    $ramBytes = (Get-CimInstance Win32_PhysicalMemory | Measure-Object -Property Capacity -Sum).Sum;
    $ramGB = [math]::Round($ramBytes / 1GB);
    $gpu = (Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name) -join ', ';
    $os = (Get-CimInstance Win32_OperatingSystem).Caption;
    $pagefiles = Get-CimInstance Win32_PageFileUsage | ForEach-Object { "$($_.Name) ($([math]::Round($_.AllocatedBaseSize / 1024, 1)) GB)" };
    $pagefileStr = $pagefiles -join ', ';
    @{
        cpu = $cpu;
        ram_gb = $ramGB;
        gpu = $gpu;
        os = $os;
        pagefile = $pagefileStr;
    } | ConvertTo-Json
    """
    
    res = subprocess.run(["pwsh", "-NoProfile", "-Command", ps_cmd], capture_output=True, text=True, encoding="utf-8")
    if res.returncode == 0 and res.stdout.strip():
        json_match = re.search(r'\{.*\}', res.stdout, re.DOTALL)
        if json_match:
            return json.loads(json_match.group(0))
    return None

def update_hardware_specs_file(specs):
    candidates = [
        os.path.join(PROJECT_ROOT, "HARDWARE_SPECS.md"),
        os.path.join(os.path.dirname(__file__), "HARDWARE_SPECS.md"),
        "HARDWARE_SPECS.md"
    ]
    file_path = None
    for p in candidates:
        if os.path.exists(p):
            file_path = p
            break

    if not file_path:
        file_path = os.path.join(PROJECT_ROOT, "HARDWARE_SPECS.md")

    with open(file_path, "r", encoding="utf-8") as f:
        content = f.read()

    dev_specs_block = f"""<dev_specs id="DEV_SPECS">
## 開発マシンスペック (Development Host Machine Specifications) [Auto-Detected]
- **CPU**: {specs.get('cpu', 'Unknown CPU')}
- **RAM**: {specs.get('ram_gb', 'Unknown')} GB Physical DDR4
- **GPU**: {specs.get('gpu', 'Unknown GPU')}
- **OS**: {specs.get('os', 'Windows')} / PowerShell 7 (`pwsh.exe`)
- **Pagefile**: {specs.get('pagefile', 'Default')}
</dev_specs>"""

    def replacer(match):
        return dev_specs_block

    new_content = re.sub(
        r'<dev_specs id="DEV_SPECS">.*?</dev_specs>',
        replacer,
        content,
        flags=re.DOTALL
    )

    with open(file_path, "w", encoding="utf-8") as f:
        f.write(new_content)

    print("Successfully auto-detected hardware specs and updated HARDWARE_SPECS.md!")
    print(dev_specs_block)

if __name__ == "__main__":
    specs = get_sys_info()
    if specs:
        update_hardware_specs_file(specs)
    else:
        print("Failed to auto-detect hardware specs.")
