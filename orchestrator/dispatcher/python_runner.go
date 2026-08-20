// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// SideEffectFn: Python Process Execution & Subprocess IO Monad
package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"flac_analyzer/orchestrator/logger"
	"flac_analyzer/orchestrator/metrics"
)

// findProjectRoot discovers the root directory containing config.toml or worker_cue.py.
func findProjectRoot() string {
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		for i := 0; i < 4; i++ {
			if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
				return dir
			}
			if _, err := os.Stat(filepath.Join(dir, "worker_cue.py")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 4; i++ {
			if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
				return dir
			}
			if _, err := os.Stat(filepath.Join(dir, "worker_cue.py")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		return wd
	}
	return "."
}

// runPythonScript launches an external Python script inside the configured virtualenv and JobObject.
// SideEffectFn: runPythonScript (IO Monad)
func (d *Dispatcher) runPythonScript(
	scriptName string,
	args []string,
	workerID int,
	role string,
	color string,
	captureStdout bool,
) (string, error) {
	parentDir := findProjectRoot()

	pythonPath := "python.exe"
	venvPython := filepath.Join(parentDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err == nil {
		pythonPath = venvPython
	} else {
		venvPythonUnix := filepath.Join(parentDir, ".venv", "bin", "python")
		if _, err := os.Stat(venvPythonUnix); err == nil {
			pythonPath = venvPythonUnix
		}
	}

	scriptPath := filepath.Join(parentDir, scriptName)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		zigScript := filepath.Join(parentDir, "zig", scriptName)
		if _, errZig := os.Stat(zigScript); errZig == nil {
			scriptPath = zigScript
		}
	}
	cmdArgs := append([]string{scriptPath}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonPath, cmdArgs...)
	cmd.Dir = parentDir

	currentCfg := d.GetConfig()
	var envVars []string
	for k, v := range currentCfg.PythonEnv {
		envVars = append(envVars, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
	}
	cmd.Env = append(os.Environ(), envVars...)

	var outBuf bytes.Buffer
	if captureStdout {
		cmd.Stdout = &outBuf
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stderr pipe for %s: %w", role, err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start %s: %w", role, err)
	}

	if cmd.Process != nil {
		if err := AssignPidToJob(cmd.Process.Pid); err != nil {
			d.LogWarn("[W-%d] AssignPidToJob note: %v", workerID, err)
		}
	}

	logger.StreamColoredLog(stderrPipe, workerID, role, color, d.getLogLevel(), d.eventLog, func(msg string) {
		metrics.AnalyzerErrorsTotal.Inc()
	})

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("%s failed: %w", role, err)
	}

	return outBuf.String(), nil
}

type pythonProfileEnvelope struct {
	Profile map[string]float64 `json:"profile"`
}

// parseAndRecordPythonProfile parses Python execution step timings and records them in StatsTracker.
func parseAndRecordPythonProfile(st *StatsTracker, component, jsonStr string) {
	if st == nil || jsonStr == "" {
		return
	}
	var env pythonProfileEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &env); err == nil && env.Profile != nil {
		for step, durSec := range env.Profile {
			st.RecordPythonStepDuration(component, step, durSec)
		}
	}
}
