import subprocess
import json
import os
import re

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
        # Find JSON block
        json_match = re.search(r'\{.*\}', res.stdout, re.DOTALL)
        if json_match:
            return json.loads(json_match.group(0))
    return None

def update_hardware_specs_file(specs):
    file_path = os.path.join(os.path.dirname(__file__), "HARDWARE_SPECS.md")
    if not os.path.exists(file_path):
        print(f"File not found: {file_path}")
        return

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
