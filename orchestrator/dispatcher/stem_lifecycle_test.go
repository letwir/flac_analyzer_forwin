package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStemLifecycleOutOfOrderReadyAndReaderBoundary(t *testing.T) {
	lifecycle, _, err := NewStemLifecycle(t.Context(), "request-7", 7, []string{"bass", "vocals", "drums"}, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	if err := lifecycle.Acquire(7, "vocals"); !errors.Is(err, ErrStemTransition) {
		t.Fatalf("unfinished stem became readable: %v", err)
	}
	if err := lifecycle.MarkReady(7, "vocals"); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkReady(7, "vocals"); !errors.Is(err, ErrStemTransition) {
		t.Fatalf("duplicate ready error=%v", err)
	}
	if err := lifecycle.Freeze(7, "vocals"); err != nil {
		t.Fatal(err)
	}
	event := <-lifecycle.ReadyEvents()
	if event.RequestID != "request-7" || event.Stem != "vocals" || event.Generation != 7 {
		t.Fatalf("event=%+v", event)
	}
	for range 2 {
		if err := lifecycle.Acquire(7, "vocals"); err != nil {
			t.Fatal(err)
		}
	}
	if err := lifecycle.Acquire(7, "vocals"); !errors.Is(err, ErrStemTransition) {
		t.Fatalf("excess reader error=%v", err)
	}
	for range 2 {
		if err := lifecycle.Release(7, "vocals"); err != nil {
			t.Fatal(err)
		}
	}
	state, refs, err := lifecycle.State(7, "vocals")
	if err != nil || state != StemReleased || refs != 0 {
		t.Fatalf("state=%d refs=%d err=%v", state, refs, err)
	}
}

func TestStemLifecycleFailureAndCancellationFailWholeGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	lifecycle, child, err := NewStemLifecycle(ctx, "request-11", 11, []string{"mix", "bass"}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	if err := lifecycle.MarkReady(11, "mix"); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-child.Done()
	for _, stem := range []string{"mix", "bass"} {
		deadline := time.Now().Add(time.Second)
		for {
			state, refs, err := lifecycle.State(11, stem)
			if err == nil && state == StemFailed && refs == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s state=%d refs=%d err=%v", stem, state, refs, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := lifecycle.MarkReady(12, "bass"); !errors.Is(err, ErrStemGeneration) {
		t.Fatalf("generation error=%v", err)
	}
}

func TestStemLifecycleExplicitFailureCancelsConsumers(t *testing.T) {
	lifecycle, child, err := NewStemLifecycle(t.Context(), "request-3", 3, []string{"mix"}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("producer failed")
	lifecycle.Fail(cause)
	<-child.Done()
	if !errors.Is(context.Cause(child), cause) {
		t.Fatalf("cause=%v", context.Cause(child))
	}
}

func TestStemLifecyclePublishRejectsStaleRequestAndBoundsPublication(t *testing.T) {
	lifecycle, child, err := NewStemLifecycle(t.Context(), "request-5", 5, []string{"mix", "bass"}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	if err := lifecycle.Publish("stale", 5, "mix"); !errors.Is(err, ErrStemRequestID) {
		t.Fatalf("stale request error=%v", err)
	}
	if err := lifecycle.Publish("request-5", 0, "mix"); !errors.Is(err, ErrStemGeneration) {
		t.Fatalf("zero generation error=%v", err)
	}
	if err := lifecycle.Publish("request-5", 5, "mix"); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Publish("request-5", 5, "bass"); !errors.Is(err, ErrStemTransition) {
		t.Fatalf("backpressure error=%v", err)
	}
	<-child.Done()
	deadline := time.Now().Add(time.Second)
	for {
		state, refs, stateErr := lifecycle.State(5, "mix")
		if stateErr == nil && state == StemFailed && refs == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mix state=%d refs=%d err=%v", state, refs, stateErr)
		}
		time.Sleep(time.Millisecond)
	}
}
