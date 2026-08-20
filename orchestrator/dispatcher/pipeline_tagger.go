// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: (Task, TrackHash, FeatureOutputs) -> TaggedFLAC (IO Monad)
package dispatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"flac_analyzer/orchestrator/logger"
)

// executeTaggerStage writes intermediate JSON payloads to queue, runs flac_tagger.py, and cleans up queue files.
// SideEffectFn: executeTaggerStage (IO Monad)
func (d *Dispatcher) executeTaggerStage(
	id int,
	task TaskPayload,
	trackHash string,
	feats *FeatureOutputs,
) error {
	baseName := filepath.Base(task.FlacPath)
	outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
	outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
	outNameTensor := fmt.Sprintf("%s_%s_tensor.json", trackHash, baseName)

	parentDir := findProjectRoot()
	queueDir := d.GetConfig().QueueDir
	if queueDir == "" {
		if parentDir != "" {
			queueDir = filepath.Join(parentDir, "queue")
		} else {
			queueDir = filepath.Join("..", "queue")
		}
	} else if !filepath.IsAbs(queueDir) {
		if parentDir != "" {
			queueDir = filepath.Join(parentDir, queueDir)
		}
	}

	if absQueueDir, err := filepath.Abs(queueDir); err == nil {
		queueDir = absQueueDir
	}

	if err := os.MkdirAll(queueDir, 0755); err != nil {
		return fmt.Errorf("failed to create queue dir: %w", err)
	}

	outPath := filepath.Join(queueDir, outName)
	outPathEss := filepath.Join(queueDir, outNameEss)
	outPathTensor := filepath.Join(queueDir, outNameTensor)

	defer cleanupQueueFiles(queueDir, trackHash, baseName)

	if err := os.WriteFile(outPathEss, []byte(feats.EssOut), 0644); err != nil {
		return fmt.Errorf("failed to write Essentia JSON: %w", err)
	}
	if err := os.WriteFile(outPathTensor, []byte(feats.TensorOut), 0644); err != nil {
		return fmt.Errorf("failed to write Tensor JSON: %w", err)
	}
	if err := os.WriteFile(outPath, []byte(feats.LibOut), 0644); err != nil {
		return fmt.Errorf("failed to write Librosa JSON: %w", err)
	}

	taggerArgs := []string{
		"--flac-path", task.FlacPath,
		"--json-path", outPath,
		"--predictions-json-path", outPathEss,
		"--tensor-json-path", outPathTensor,
	}
	if task.TrackNumber > 0 {
		taggerArgs = append(taggerArgs, "--prefix", fmt.Sprintf("CUE_TRACK%02d", task.TrackNumber))
	}

	taggerStart := time.Now()
	tagOut, tagErr := d.runPythonScript("flac_tagger.py", taggerArgs, id, "FlacTagger", logger.ColorGreen, true)
	if d.statsTracker != nil {
		d.statsTracker.RecordStageDuration("flac_tagger", time.Since(taggerStart))
		parseAndRecordPythonProfile(d.statsTracker, "tagger", tagOut)
	}
	if tagErr != nil {
		d.LogWarn("[W-%d] FLAC tagger warned/failed for %s: %v", id, task.FlacPath, tagErr)
	}

	return nil
}
