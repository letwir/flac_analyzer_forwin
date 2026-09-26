$content = Get-Content orchestrator/dispatcher/admission.go -Raw
$content = $content -replace 'ErrInvalidAdmissionPlan = errors.New\("invalid admission plan"\)', "ErrInvalidAdmissionPlan = errors.New(`"invalid admission plan`")`n`tErrPermanentBudgetExcess = errors.New(`"task resource requirements exceed total node capacity`")"

$content = $content -replace '(?s)func subtractAdmissionPlan\(total, used AdmissionPlan\) AdmissionPlan \{.*?\n\}', "func safeSub(a, b uint64) uint64 {
`tif (a < b) { return 0 }
`treturn a - b
}

func intSafeSub(a, b int) int {
`tif (a < b) { return 0 }
`treturn a - b
}

func subtractAdmissionPlan(total, used AdmissionPlan) AdmissionPlan {
`treturn AdmissionPlan{
`t`tHostRAMBytes:       safeSub(total.HostRAMBytes, used.HostRAMBytes),
`t`tDedicatedVRAMBytes: safeSub(total.DedicatedVRAMBytes, used.DedicatedVRAMBytes),
`t`tCPULanes:           intSafeSub(total.CPULanes, used.CPULanes),
`t`tGPULanes:           intSafeSub(total.GPULanes, used.GPULanes),
`t`tDemucsSlots:        intSafeSub(total.DemucsSlots, used.DemucsSlots),
`t`tDiskTempBytes:      safeSub(total.DiskTempBytes, used.DiskTempBytes),
`t}
}"

$content = $content -replace '(?s)func \(c \*AdmissionController\) AtomicReserve\(plan AdmissionPlan\) \(AdmissionLease, error\) \{.*?available := subtractAdmissionPlan\(c\.capacity, c\.used\)', 'func (c *AdmissionController) AtomicReserve(plan AdmissionPlan) (AdmissionLease, error) {
	if c == nil {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrInvalidAdmissionPlan)
	}
	if err := validateAdmissionPlan(plan); err != nil {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if !fitsAdmissionPlan(plan, c.capacity) {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrPermanentBudgetExcess)
	}
	available := subtractAdmissionPlan(c.capacity, c.used)'

$content = $content -replace '(?s)return taskAdmissionPlan\{\s*request: AdmissionPlan\{\s*HostRAMBytes:  taskRAM,\s*DiskTempBytes: diskBytes,\s*\},', "return taskAdmissionPlan{
`t`trequest: AdmissionPlan{
`t`t`tHostRAMBytes:  taskRAM,
`t`t`tDiskTempBytes: diskBytes,
`t`t`tDemucsSlots:   1,
`t`t},"

$content = $content -replace '(?s)return controller.AtomicReserve\(AdmissionPlan\{\s*DedicatedVRAMBytes: vram,\s*CPULanes:           1,\s*GPULanes:           1,\s*DemucsSlots:        1,\s*\}\)', "return controller.AtomicReserve(AdmissionPlan{
`t`tDedicatedVRAMBytes: vram,
`t`tCPULanes:           1,
`t`tGPULanes:           1,
`t`tDemucsSlots:        0,
`t})"

Set-Content orchestrator/dispatcher/admission.go $content
