// Package config provides pure data models, TOML parsing, and dynamic hardware scaling functors.
// PureMorph: Map[string]string x CPU x Workers -> Map[string]string
package config

import (
	"strconv"
)

// ResolvePythonEnv derives environment variable mappings deterministically without mutating input.
// PureMorph: ResolvePythonEnv
func ResolvePythonEnv(raw map[string]string, numCPU, numWorkers int) map[string]string {
	resolved := make(map[string]string, len(raw))
	for k, v := range raw {
		if v != "0" {
			resolved[k] = v
			continue
		}
		threads := 1
		if numWorkers > 0 && numCPU > 0 {
			threads = numCPU / numWorkers
		}
		if threads < 1 {
			threads = 1
		}
		resolved[k] = strconv.Itoa(threads)
	}
	return resolved
}
