// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Objects: TaskPayload, FlexibleString, and Type Aliases for Category Isomorphism
package dispatcher

import (
	"encoding/json"
	"strings"

	"flac_analyzer/orchestrator/config"
	"flac_analyzer/orchestrator/logger"
)

// Category Isomorphism: Type aliases to preserve functor mapping from config and logger packages
type (
	Config      = config.Config
	LogLevel    = logger.LogLevel
	EventLogger = logger.EventLogger
)

const (
	LevelDebug = logger.LevelDebug
	LevelInfo  = logger.LevelInfo
	LevelWarn  = logger.LevelWarn
	LevelError = logger.LevelError

	ColorReset        = logger.ColorReset
	ColorLevel1Dim    = logger.ColorLevel1Dim
	ColorLevel2Blue   = logger.ColorLevel2Blue
	ColorLevel3Purple = logger.ColorLevel3Purple
	ColorLevel4Cyan   = logger.ColorLevel4Cyan
	ColorLevel5Green  = logger.ColorLevel5Green
	ColorLevel6Bright = logger.ColorLevel6Bright
	ColorWarn         = logger.ColorWarn
	ColorError        = logger.ColorError

	ColorRed    = logger.ColorRed
	ColorGreen  = logger.ColorGreen
	ColorYellow = logger.ColorYellow
	ColorBlue   = logger.ColorBlue
	ColorCyan   = logger.ColorCyan
	ColorPurple = logger.ColorPurple
)

// ParseLogLevel exposes the logger.ParseLogLevel pure morphism.
var ParseLogLevel = logger.ParseLogLevel

// TaskPayload represents an individual track analysis request within the task queue.
type TaskPayload struct {
	FlacPath     string `json:"flacPath"`
	FileSize     int64  `json:"fileSize"`
	TargetScript string `json:"targetScript"`
	TrackNumber  int    `json:"trackNumber"`
	StartSample  int64  `json:"startSample"`
	EndSample    int64  `json:"endSample"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	AlbumArtist  string `json:"albumArtist"`
	Force        bool   `json:"force"`
}

// FlexibleString decodes JSON fields that may be represented as a single string or an array of strings.
type FlexibleString string

func (fs *FlexibleString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*fs = FlexibleString(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*fs = FlexibleString(strings.Join(arr, " / "))
		return nil
	}
	*fs = FlexibleString(string(data))
	return nil
}

func (fs FlexibleString) String() string {
	return string(fs)
}
