// Package logger provides structured CUI color streaming, log levels, and Windows EventLog bindings.
// Mor: io.ReadCloser -> Stream(CUI x EventLog x Metrics)
package logger

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"strings"
)

// EventLogger defines the Windows Event Log interface for decoupled integration.
type EventLogger interface {
	Info(eid uint32, msg string) error
	Warning(eid uint32, msg string) error
	Error(eid uint32, msg string) error
}

// StreamColoredLog reads stderr from child Python processes line by line,
// filters ONNX runtime noise, applies ANSI color themes, and dispatches to EventLog / metrics callback.
// SideEffectFn: StreamColoredLog (IO Monad)
func StreamColoredLog(
	pipe io.ReadCloser,
	workerID int,
	role string,
	color string,
	minLevel LogLevel,
	elog EventLogger,
	onErr func(msg string),
) {
	scanner := bufio.NewScanner(pipe)
	prefix := fmt.Sprintf("%s[W-%d] [%s] ", color, workerID, role)

	for scanner.Scan() {
		line := scanner.Text()

		// ONNX Runtime の内部 Fallback 警告などのノイズは通常ログではサイレント（DEBUG レベルのみ）にいたしますわ
		if strings.Contains(line, "running in Fallback mode") || strings.Contains(line, "onnxruntime::cuda::Conv") {
			if minLevel <= LevelDebug {
				log.Printf("[W-%d] [%s] %s", workerID, role, line)
			}
			continue
		}

		isError := strings.Contains(line, "[ERROR]") ||
			strings.Contains(strings.ToLower(line), "error") ||
			strings.Contains(strings.ToLower(line), "traceback")

		if isError {
			msg := fmt.Sprintf("[W-%d] [%s] %s", workerID, role, line)
			fmt.Printf("%s%s%s\n", ColorRed, msg, ColorReset)
			if elog != nil {
				_ = elog.Error(1003, msg)
			}
			if onErr != nil {
				onErr(msg)
			}
			continue
		}

		if minLevel <= LevelInfo {
			fmt.Printf("%s%s%s\n", prefix, line, ColorReset)
		}
	}
}
