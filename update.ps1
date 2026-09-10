# Update binaries from this checkout; -CheckOnly runs tests/build without installation.
[CmdletBinding()]
param([switch]$CheckOnly)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = $PSScriptRoot
$python = Join-Path $root '.venv\Scripts\python.exe'
$targets = @(
    (Join-Path $root 'orchestrator.exe'),
    (Join-Path $root 'single-orchestrator.exe'),
    (Join-Path $root 'orchestrator\orchestrator.exe')
)

function Assert-Stopped {
    $guardPaths = $targets + (Join-Path $root 'orchestrator\flac_analyzer.exe')
    $active = @(Get-Process | Where-Object { $_.Path -and $_.Path -in $guardPaths })
    if ($active.Count -gt 0) {
        throw "Stop the analyzer before updating (PID: $($active.Id -join ', '))."
    }
}

$stage = $null
$installed = @()
$backups = @{}
try {
    if (-not (Test-Path -LiteralPath $python -PathType Leaf)) {
        throw 'Missing .venv\Scripts\python.exe. Set up the Python environment first.'
    }
    $go = (Get-Command go.exe -ErrorAction Stop).Source
    if (-not $CheckOnly) { Assert-Stopped }
    Push-Location $root
    try {
        & $python -B -m unittest test_flac_tagger test_fix_empty_json
        if ($LASTEXITCODE -ne 0) { throw 'FLAC tagger tests failed.' }
        Push-Location (Join-Path $root 'orchestrator')
        try {
            & $go test ./...
            if ($LASTEXITCODE -ne 0) { throw 'Go tests failed.' }
            $stage = Join-Path $root ('.update-' + [guid]::NewGuid().ToString('N'))
            New-Item -ItemType Directory -Path $stage | Out-Null
            $built = Join-Path $stage 'orchestrator.exe'
            & $go build -o $built .
            if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
            $helpText = (& $built -h 2>&1) -join "`n"
            if ($LASTEXITCODE -ne 0 -or $helpText -notmatch '-fix-record') {
                throw 'Built binary did not expose the repair command.'
            }
        } finally { Pop-Location }
    } finally { Pop-Location }

    $expected = (Get-FileHash -LiteralPath $built -Algorithm SHA256).Hash
    if ($CheckOnly) {
        Write-Host "Checks and build passed. No installed binary changed. SHA256: $expected"
    } else {
        Assert-Stopped
        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fffffff'
        # Finish every backup before replacing any target.
        foreach ($target in $targets) {
            if (Test-Path -LiteralPath $target) {
                $backup = "$target.$stamp.bak"
                Copy-Item -LiteralPath $target -Destination $backup
                if ((Get-FileHash -LiteralPath $backup).Hash -ne (Get-FileHash -LiteralPath $target).Hash) {
                    throw "Backup verification failed: $target"
                }
                $backups[$target] = $backup
            }
        }
        foreach ($target in $targets) {
            # Include a partially copied target in rollback if the copy fails.
            $installed += $target
            Copy-Item -LiteralPath $built -Destination $target -Force
            if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne $expected) {
                throw "Installed hash mismatch: $target"
            }
        }
        Write-Host "Updated all 3 binaries. SHA256: $expected"
        foreach ($backup in $backups.Values) { Write-Host "Backup: $backup" }
        Write-Host 'Start the analyzer when ready. Existing records are not reprocessed automatically.'
    }
} catch {
    Write-Error -Message $_ -ErrorAction Continue
    foreach ($target in $installed) {
        try {
            if ($backups.ContainsKey($target)) {
                Copy-Item -LiteralPath $backups[$target] -Destination $target -Force
            } else {
                Remove-Item -LiteralPath $target -Force
            }
        } catch {
            Write-Error "Rollback failed for $target. Backups remain available. $_" -ErrorAction Continue
        }
    }
    exit 1
} finally {
    if ($stage -and (Test-Path -LiteralPath $stage)) {
        # Only remove the unique build file and its now-empty directory.
        $candidate = Join-Path $stage 'orchestrator.exe'
        if (Test-Path -LiteralPath $candidate) { Remove-Item -LiteralPath $candidate -Force }
        Remove-Item -LiteralPath $stage
    }
}
