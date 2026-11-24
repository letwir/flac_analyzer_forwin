package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rolloutGateTrackInput struct {
	TrackID  string  `json:"track_id"`
	FilePath string  `json:"file_path"`
	Shape    []int64 `json:"shape"`
	Dtype    string  `json:"dtype"`
}

type rolloutGateManifest struct {
	PythonPath string                  `json:"python_path"`
	RepoRoot   string                  `json:"repo_root"`
	SR         int                     `json:"sr"`
	Tracks     []rolloutGateTrackInput `json:"tracks"`
}

type rolloutGateTrackReport struct {
	TrackID     string          `json:"track_id"`
	SpectroUsed bool            `json:"spectro_used"`
	Librosa     json.RawMessage `json:"librosa"`
	Tensor      json.RawMessage `json:"tensor"`
	Essentia    json.RawMessage `json:"essentia"`
}

type rolloutGateCapReport struct {
	ConfiguredCap         int                      `json:"configured_cap"`
	ResolvedCap           int                      `json:"resolved_cap"`
	MaxObservedConcurrent int32                    `json:"max_observed_concurrent"`
	OrphanCount           int                      `json:"orphan_count"`
	Tracks                []rolloutGateTrackReport `json:"tracks"`
}

func updateMaxInt32(target *atomic.Int32, candidate int32) {
	for {
		cur := target.Load()
		if candidate <= cur {
			return
		}
		if target.CompareAndSwap(cur, candidate) {
			return
		}
	}
}

func TestPhaseDRolloutGateDownstream1Vs4(t *testing.T) {
	gateDir := os.Getenv("FLAC_PHASE_D_GATE_DIR")
	if gateDir == "" {
		t.Skip("FLAC_PHASE_D_GATE_DIR not set")
	}

	manifestBytes, err := os.ReadFile(filepath.Join(gateDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manifest rolloutGateManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	if len(manifest.Tracks) != 4 {
		t.Fatalf("expected 4 tracks in manifest, got %d", len(manifest.Tracks))
	}

	const gib = uint64(1024 * 1024 * 1024)
	baseEnv := os.Environ()

	for _, configuredCap := range []int{1, 4} {
		resolvedCap := ResolveDownstreamTracksCapPure(configuredCap, 4, 32*gib, 2*gib)
		if resolvedCap != configuredCap {
			t.Fatalf("ResolveDownstreamTracksCapPure(%d) = %d, want %d", configuredCap, resolvedCap, configuredCap)
		}

		controller, err := NewAdmissionController(AdmissionCapacity{
			HostRAMBytes:     32 * gib,
			DownstreamTracks: resolvedCap,
			DiskTempBytes:    32 * gib,
		})
		if err != nil {
			t.Fatalf("NewAdmissionController(cap=%d): %v", resolvedCap, err)
		}

		cpuPool := NewWorkerDaemonPool(
			resolvedCap,
			manifest.PythonPath,
			manifest.RepoRoot,
			baseEnv,
			t.Logf,
			WorkerDaemonRoleCPU,
		)
		gpuPool := NewWorkerDaemonPool(
			1,
			manifest.PythonPath,
			manifest.RepoRoot,
			baseEnv,
			t.Logf,
			WorkerDaemonRoleFeatureGPU,
		)

		spectroDir := filepath.Join(gateDir, "spectro_cap_"+string(rune('0'+resolvedCap)))
		if err := os.MkdirAll(spectroDir, 0o755); err != nil {
			cpuPool.Close()
			gpuPool.Close()
			t.Fatalf("MkdirAll spectroDir: %v", err)
		}

		runCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		var activeDownstream atomic.Int32
		var maxObserved atomic.Int32
		results := make([]rolloutGateTrackReport, len(manifest.Tracks))
		errs := make([]error, len(manifest.Tracks))

		var wg sync.WaitGroup
		for idx, tr := range manifest.Tracks {
			wg.Add(1)
			go func(idx int, tr rolloutGateTrackInput) {
				defer wg.Done()

				var lease AdmissionLease
				for {
					l, reserveErr := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{
						HostRAMBytes:     2 * gib,
						DownstreamTracks: 1,
					})
					if reserveErr == nil {
						lease = l
						break
					}
					if !errors.Is(reserveErr, ErrAdmissionUnavailable) {
						errs[idx] = reserveErr
						return
					}
					select {
					case <-runCtx.Done():
						errs[idx] = runCtx.Err()
						return
					case <-time.After(5 * time.Millisecond):
					}
				}
				defer controller.Release(lease)

				cur := activeDownstream.Add(1)
				updateMaxInt32(&maxObserved, cur)
				defer activeDownstream.Add(-1)

				specPath := filepath.Join(spectroDir, tr.TrackID+".spectro.npy")
				defer func() {
					_ = os.Remove(specPath)
				}()

				gpuPayload := ExtractAllPayload{
					SR:        manifest.SR,
					TrackHash: tr.TrackID,
					Stems: map[string]StemInfo{
						"mix": {
							StorageType: "file",
							FilePath:    tr.FilePath,
							SpectroPath: specPath,
							Shape:       tr.Shape,
							Dtype:       tr.Dtype,
						},
					},
				}

				gpuClient, err := gpuPool.Acquire(runCtx)
				if err != nil {
					errs[idx] = err
					return
				}
				gpuResp, gpuErr := gpuClient.ExtractGPU(runCtx, gpuPayload)
				if releaseErr := gpuPool.ReleaseChecked(gpuClient); releaseErr != nil && gpuErr == nil {
					gpuErr = releaseErr
				}
				if gpuErr != nil {
					errs[idx] = gpuErr
					return
				}

				outcome, ok := gpuResp.SpectroCache["mix"]
				spectroUsed := ok && outcome.Status == SpectroCacheStatusWritten && outcome.SpectroPath == specPath
				cpuStem := StemInfo{
					StorageType: "file",
					FilePath:    tr.FilePath,
					Shape:       tr.Shape,
					Dtype:       tr.Dtype,
				}
				if spectroUsed {
					cpuStem.SpectroPath = specPath
				}
				cpuPayload := ExtractAllPayload{
					SR:        manifest.SR,
					TrackHash: tr.TrackID,
					Stems:     map[string]StemInfo{"mix": cpuStem},
				}

				cpuClient, err := cpuPool.Acquire(runCtx)
				if err != nil {
					errs[idx] = err
					return
				}
				cpuResp, cpuErr := cpuClient.ExtractCPU(runCtx, cpuPayload)
				if releaseErr := cpuPool.ReleaseChecked(cpuClient); releaseErr != nil && cpuErr == nil {
					cpuErr = releaseErr
				}
				if cpuErr != nil {
					errs[idx] = cpuErr
					return
				}

				joined, joinErr := joinFeatureLaneResponses(cpuResp, gpuResp)
				if joinErr != nil {
					errs[idx] = joinErr
					return
				}

				results[idx] = rolloutGateTrackReport{
					TrackID:     tr.TrackID,
					SpectroUsed: spectroUsed,
					Librosa:     json.RawMessage(joined.LibOut),
					Tensor:      json.RawMessage(joined.TensorOut),
					Essentia:    json.RawMessage(joined.EssOut),
				}
			}(idx, tr)
		}
		wg.Wait()
		cancel()
		cpuPool.Close()
		gpuPool.Close()

		for i, runErr := range errs {
			if runErr != nil {
				t.Fatalf("cap=%d track[%d] failed: %v", resolvedCap, i, runErr)
			}
			if !results[i].SpectroUsed {
				t.Fatalf("cap=%d track[%d] did not use spectro cache", resolvedCap, i)
			}
		}

		if got := controller.Usage().DownstreamTracks; got != 0 {
			t.Fatalf("cap=%d DownstreamTracks after drain = %d, want 0", resolvedCap, got)
		}
		entries, err := os.ReadDir(spectroDir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", spectroDir, err)
		}
		if len(entries) != 0 {
			t.Fatalf("cap=%d left %d orphaned spectro files", resolvedCap, len(entries))
		}

		report := rolloutGateCapReport{
			ConfiguredCap:         configuredCap,
			ResolvedCap:           resolvedCap,
			MaxObservedConcurrent: maxObserved.Load(),
			OrphanCount:           len(entries),
			Tracks:                results,
		}
		reportBytes, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal report_cap_%d: %v", resolvedCap, err)
		}
		reportPath := filepath.Join(gateDir, "report_cap_"+string(rune('0'+resolvedCap))+".json")
		if err := os.WriteFile(reportPath, reportBytes, 0o644); err != nil {
			t.Fatalf("write %s: %v", reportPath, err)
		}
	}
}
