// Package logger provides structured CUI color streaming, log levels, and Windows EventLog bindings.
// Semantics(Category): ANSI Color Objects & Presets
package logger

const (
	ColorReset        = "\033[0m"
	ColorLevel1Dim    = "\033[2;37m" // Dim Gray (Identity / Check) - 最暗
	ColorLevel2Blue   = "\033[34m"   // Blue (SHM Allocation) - 暗め
	ColorLevel3Purple = "\033[35m"   // Magenta (Demucs Isolation) - 中暗
	ColorLevel4Cyan   = "\033[36m"   // Cyan (Feature Extract) - 中明
	ColorLevel5Green  = "\033[32m"   // Green (DB Ingestion) - 明
	ColorLevel6Bright = "\033[1;97m" // Bold Bright White (Tag & Complete) - 最光
	ColorWarn         = "\033[1;33m" // Bold Yellow (WARN専用)
	ColorError        = "\033[1;31m" // Bold Red (ERROR専用)

	// Legacy alias compatibility
	ColorRed    = "\033[1;31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[1;33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorPurple = "\033[35m"
)
