package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTwoStageWavefrontGPUtoCPUFallbackAndCleanupAfterWait(t *testing.T) {
	stems := []string{"mix", "bass", "drums", "other"}
	spectroDir := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "outside.spectro.npy")
	if err := os.WriteFile(outsideFile, []byte("do-not-delete"), 0644); err != nil {
		t.Fatal(err)
	}
	lifecycle, child, err := NewStemLifecycle(t.Context(), "req-twostage-1", 401, stems, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	var mu sync.Mutex
	cpuReceivedSpectro := make(map[string]string)
	gpuOrder := make([]string, 0, len(stems))
	cpuOrder := make([]string, 0, len(stems))

	wf, err := newTwoStageStemWavefront(child, len(stems), lifecycle, 1, spectroDir, true, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		for stem, info := range payload.Stems {
			if lane == FeatureLaneGPU {
				mu.Lock()
				gpuOrder = append(gpuOrder, stem)
				mu.Unlock()
				switch stem {
				case "mix":
					if err := os.WriteFile(info.SpectroPath, []byte("mock-npy"), 0644); err != nil {
						return nil, err
					}
					return &DaemonResponse{
						Tensor: map[string]interface{}{stem: map[string]interface{}{"ok": true}},
						SpectroCache: map[string]SpectroCacheOutcome{
							stem: {Status: SpectroCacheStatusWritten, SpectroPath: info.SpectroPath},
						},
					}, nil
				case "bass":
					// Simulate GPU fallback: CPU must receive empty SpectroPath to fall back to CPU STFT.
					return &DaemonResponse{
						Tensor: map[string]interface{}{stem: map[string]interface{}{"ok": true}},
						SpectroCache: map[string]SpectroCacheOutcome{
							stem: {Status: SpectroCacheStatusFallback, Error: "simulated OOM"},
						},
					}, nil
				case "other":
					// Worker returned a mismatched path outside the allocated request directory:
					// must fall back to CPU STFT and NEVER delete outsideFile during cleanup.
					return &DaemonResponse{
						Tensor: map[string]interface{}{stem: map[string]interface{}{"ok": true}},
						SpectroCache: map[string]SpectroCacheOutcome{
							stem: {Status: SpectroCacheStatusWritten, SpectroPath: outsideFile},
						},
					}, nil
				default:
					// "drums" omits SpectroCache outcome -> also falls back cleanly to CPU STFT.
					return &DaemonResponse{
						Tensor: map[string]interface{}{stem: map[string]interface{}{"ok": true}},
					}, nil
				}
			}
			// CPU lane
			mu.Lock()
			cpuOrder = append(cpuOrder, stem)
			cpuReceivedSpectro[stem] = info.SpectroPath
			mu.Unlock()
			return &DaemonResponse{
				Librosa:  map[string]interface{}{stem: map[string]interface{}{"ok": true}},
				Essentia: map[string]interface{}{"ok": true},
			}, nil
		}
		return &DaemonResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, stem := range stems {
		if err := wf.Publish(DemucsStemReadyEvent{
			Status:     "stem_ready",
			RequestID:  "req-twostage-1",
			Generation: 401,
			Stem:       stem,
			AudioHash:  "hash-twostage",
			SR:         44100,
			Info:       StemInfo{Shape: []int64{1, 1024}, Dtype: "float32"},
		}); err != nil {
			t.Fatalf("Publish(%s) failed: %v", stem, err)
		}
	}
	wf.CloseInput()

	cpuResp, gpuResp, hash, sr, waitErr := wf.Wait()
	if waitErr != nil {
		t.Fatalf("Wait() failed: %v", waitErr)
	}
	if hash != "hash-twostage" || sr != 44100 || len(cpuResp.Librosa) != 4 || len(gpuResp.Tensor) != 4 {
		t.Fatalf("unexpected Wait() result hash=%q sr=%d cpu=%+v gpu=%+v", hash, sr, cpuResp, gpuResp)
	}

	expectedMixPath := filepath.Join(spectroDir, "req-twostage-1-mix.spectro.npy")
	if cpuReceivedSpectro["mix"] != expectedMixPath {
		t.Fatalf("expected CPU mix SpectroPath=%q, got %q", expectedMixPath, cpuReceivedSpectro["mix"])
	}
	if cpuReceivedSpectro["bass"] != "" {
		t.Fatalf("expected CPU bass SpectroPath cleared on fallback, got %q", cpuReceivedSpectro["bass"])
	}
	if cpuReceivedSpectro["drums"] != "" {
		t.Fatalf("expected CPU drums SpectroPath cleared when omitted, got %q", cpuReceivedSpectro["drums"])
	}
	if cpuReceivedSpectro["other"] != "" {
		t.Fatalf("expected CPU other SpectroPath cleared on path mismatch, got %q", cpuReceivedSpectro["other"])
	}
	if len(gpuOrder) != 4 || len(cpuOrder) != 4 {
		t.Fatalf("unexpected stem counts gpu=%v cpu=%v", gpuOrder, cpuOrder)
	}

	// File must still exist before CleanupSpectroCache(), then be removed after Wait().
	if _, err := os.Stat(expectedMixPath); err != nil {
		t.Fatalf("expected cache file to survive until post-Wait cleanup: %v", err)
	}
	if err := wf.CleanupSpectroCache(); err != nil {
		t.Fatalf("CleanupSpectroCache failed: %v", err)
	}
	if _, err := os.Stat(expectedMixPath); !os.IsNotExist(err) {
		t.Fatalf("expected cache file removed after CleanupSpectroCache, err=%v", err)
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outsideFile must NEVER be deleted on mismatched worker response: %v", err)
	}
}

func TestTwoStageWavefrontMixBeforeVocalsOrdering(t *testing.T) {
	// Publish "vocals" BEFORE "mix" with multiple CPU consumers; CPU "vocals" must still
	// wait until CPU "mix" finishes so mix_predictions ordering holds.
	stems := []string{"vocals", "mix"}
	spectroDir := t.TempDir()
	lifecycle, child, err := NewStemLifecycle(t.Context(), "req-order-1", 402, stems, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	var mu sync.Mutex
	cpuEvents := make([]string, 0, 4)
	mixStarted := make(chan struct{})
	allowMixFinish := make(chan struct{})
	var closeAllowOnce sync.Once
	releaseMix := func() {
		closeAllowOnce.Do(func() { close(allowMixFinish) })
	}
	defer releaseMix()

	wf, err := newTwoStageStemWavefront(child, len(stems), lifecycle, 2, spectroDir, true, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		for stem := range payload.Stems {
			if lane == FeatureLaneGPU {
				return &DaemonResponse{Tensor: map[string]interface{}{stem: true}}, nil
			}
			mu.Lock()
			cpuEvents = append(cpuEvents, "start:"+stem)
			mu.Unlock()
			if stem == "mix" {
				close(mixStarted)
				select {
				case <-allowMixFinish:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			mu.Lock()
			cpuEvents = append(cpuEvents, "done:"+stem)
			mu.Unlock()
			return &DaemonResponse{Librosa: map[string]interface{}{stem: true}, Essentia: map[string]interface{}{"ok": true}}, nil
		}
		return &DaemonResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, stem := range stems {
		if err := wf.Publish(DemucsStemReadyEvent{
			Status:     "stem_ready",
			RequestID:  "req-order-1",
			Generation: 402,
			Stem:       stem,
			AudioHash:  "hash-order",
			SR:         44100,
			Info:       StemInfo{Shape: []int64{1, 1024}, Dtype: "float32"},
		}); err != nil {
			t.Fatalf("Publish(%s) failed: %v", stem, err)
		}
	}
	wf.CloseInput()

	select {
	case <-mixStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CPU mix stage to start")
	}
	// While mix is running, vocals must NOT have started on CPU yet.
	mu.Lock()
	eventsSnap := append([]string(nil), cpuEvents...)
	mu.Unlock()
	for _, ev := range eventsSnap {
		if ev == "start:vocals" {
			t.Fatalf("vocals started on CPU before mix finished: %v", eventsSnap)
		}
	}

	releaseMix()
	if _, _, _, _, waitErr := wf.Wait(); waitErr != nil {
		t.Fatalf("Wait() failed: %v", waitErr)
	}

	mu.Lock()
	defer mu.Unlock()
	// Verify done:mix appears strictly before start:vocals.
	doneMixIdx, startVocalsIdx := -1, -1
	for i, ev := range cpuEvents {
		if ev == "done:mix" {
			doneMixIdx = i
		} else if ev == "start:vocals" {
			startVocalsIdx = i
		}
	}
	if doneMixIdx == -1 || startVocalsIdx == -1 || doneMixIdx >= startVocalsIdx {
		t.Fatalf("expected done:mix before start:vocals, got events=%v", cpuEvents)
	}
}

func TestTwoStageWavefrontSingleCPUConsumerVocalsBeforeMixNoDeadlock(t *testing.T) {
	stems := []string{"vocals", "mix"}
	spectroDir := t.TempDir()
	lifecycle, child, err := NewStemLifecycle(t.Context(), "req-order-single", 403, stems, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	var mu sync.Mutex
	cpuOrder := make([]string, 0, 2)
	wf, err := newTwoStageStemWavefront(child, len(stems), lifecycle, 1, spectroDir, true, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		for stem := range payload.Stems {
			if lane == FeatureLaneGPU {
				return &DaemonResponse{Tensor: map[string]interface{}{stem: true}}, nil
			}
			mu.Lock()
			cpuOrder = append(cpuOrder, stem)
			mu.Unlock()
			return &DaemonResponse{Librosa: map[string]interface{}{stem: true}, Essentia: map[string]interface{}{"ok": true}}, nil
		}
		return &DaemonResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, stem := range stems {
		if err := wf.Publish(DemucsStemReadyEvent{
			Status:     "stem_ready",
			RequestID:  "req-order-single",
			Generation: 403,
			Stem:       stem,
			AudioHash:  "hash-single",
			SR:         44100,
			Info:       StemInfo{Shape: []int64{1, 1024}, Dtype: "float32"},
		}); err != nil {
			t.Fatalf("Publish(%s) failed: %v", stem, err)
		}
	}
	wf.CloseInput()

	waitDone := make(chan error, 1)
	go func() {
		_, _, _, _, wErr := wf.Wait()
		waitDone <- wErr
	}()

	select {
	case wErr := <-waitDone:
		if wErr != nil {
			t.Fatalf("Wait() failed with cpuConsumers=1: %v", wErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("single CPU consumer wavefront deadlocked when vocals published before mix")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(cpuOrder) != 2 || cpuOrder[0] != "mix" || cpuOrder[1] != "vocals" {
		t.Fatalf("expected CPU execution order [mix vocals], got %v", cpuOrder)
	}
}

func TestTwoStageWavefrontMissingMixFailsWithoutRunningVocals(t *testing.T) {
	stems := []string{"vocals", "mix"}
	spectroDir := t.TempDir()
	lifecycle, child, err := NewStemLifecycle(t.Context(), "req-missing-mix", 404, stems, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	var mu sync.Mutex
	cpuRanVocals := false
	wf, err := newTwoStageStemWavefront(child, len(stems), lifecycle, 1, spectroDir, true, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		for stem := range payload.Stems {
			if lane == FeatureLaneGPU {
				return &DaemonResponse{Tensor: map[string]interface{}{stem: true}}, nil
			}
			mu.Lock()
			if stem == "vocals" {
				cpuRanVocals = true
			}
			mu.Unlock()
			return &DaemonResponse{Librosa: map[string]interface{}{stem: true}, Essentia: map[string]interface{}{"ok": true}}, nil
		}
		return &DaemonResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Publish only vocals and close input without publishing expected mix.
	if err := wf.Publish(DemucsStemReadyEvent{
		Status:     "stem_ready",
		RequestID:  "req-missing-mix",
		Generation: 404,
		Stem:       "vocals",
		AudioHash:  "hash-missing-mix",
		SR:         44100,
		Info:       StemInfo{Shape: []int64{1, 1024}, Dtype: "float32"},
	}); err != nil {
		t.Fatalf("Publish(vocals) failed: %v", err)
	}
	wf.CloseInput()

	if _, _, _, _, waitErr := wf.Wait(); waitErr == nil {
		t.Fatal("expected Wait() to fail when input closed without expected mix stem")
	}

	mu.Lock()
	defer mu.Unlock()
	if cpuRanVocals {
		t.Fatal("CPU vocals must not run when expected mix stem was never published")
	}
}
