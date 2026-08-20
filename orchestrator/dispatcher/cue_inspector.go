// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: FlacFilePath -> (CueInspectResult, Error)
package dispatcher

import (
	"encoding/json"
	"fmt"
	"strings"

	"flac_analyzer/orchestrator/logger"
)

// CueInspectTrack represents a single track entry within a parsed CUE sheet or embedded cuesheet.
type CueInspectTrack struct {
	TrackNumber int            `json:"track_number"`
	StartSample int64          `json:"start_sample"`
	EndSample   int64          `json:"end_sample"`
	Title       FlexibleString `json:"title"`
	Artist      FlexibleString `json:"artist"`
}

// CueInspectResult represents the full album metadata and track list extracted from CUE inspection.
type CueInspectResult struct {
	Status      string            `json:"status"`
	Filepath    string            `json:"filepath"`
	Album       FlexibleString    `json:"album"`
	AlbumArtist FlexibleString    `json:"album_artist"`
	Tracks      []CueInspectTrack `json:"tracks"`
}

// InspectCue calls worker_cue.py to extract track boundaries and metadata for a given FLAC file.
// SideEffectFn: InspectCue (IO Monad)
func (d *Dispatcher) InspectCue(flacPath string) (*CueInspectResult, error) {
	out, err := d.runPythonScript("worker_cue.py", []string{
		"--flac-path", flacPath,
	}, 0, "CueInspect", logger.ColorCyan, true)
	if err != nil {
		return nil, fmt.Errorf("failed to execute worker_cue.py: %w", err)
	}

	var res CueInspectResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return nil, fmt.Errorf("failed to parse CueInspect JSON: %w (raw: %s)", err, out)
	}
	return &res, nil
}
