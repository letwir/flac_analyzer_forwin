// benchmark-acceptance compares two recorded benchmark windows offline.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"flac_analyzer/orchestrator/benchmark"
)

func main() {
	beforePath := flag.String("before", "", "path to the baseline MeasurementWindow JSON")
	afterPath := flag.String("after", "", "path to the candidate MeasurementWindow JSON")
	limitsPath := flag.String("limits", "", "path to the AcceptanceLimits JSON")
	flag.Parse()
	if *beforePath == "" || *afterPath == "" || *limitsPath == "" {
		fmt.Fprintln(os.Stderr, "usage: benchmark-acceptance -before before.json -after after.json -limits limits.json")
		os.Exit(2)
	}

	before, err := readJSON[benchmark.MeasurementWindow](*beforePath)
	if err != nil {
		fail(err)
	}
	after, err := readJSON[benchmark.MeasurementWindow](*afterPath)
	if err != nil {
		fail(err)
	}
	limits, err := readJSON[benchmark.AcceptanceLimits](*limitsPath)
	if err != nil {
		fail(err)
	}

	evaluation, err := benchmark.Evaluate(before, after, limits)
	if err != nil {
		fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(evaluation); err != nil {
		fail(fmt.Errorf("write evaluation: %w", err))
	}
	if !evaluation.Passed {
		os.Exit(1)
	}
}

func readJSON[T any](path string) (T, error) {
	var value T
	file, err := os.Open(path)
	if err != nil {
		return value, fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode %q: %w", path, err)
	}
	if err := requireEOF(decoder); err != nil {
		return value, fmt.Errorf("decode %q: %w", path, err)
	}
	return value, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values are not allowed")
	}
	return err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
