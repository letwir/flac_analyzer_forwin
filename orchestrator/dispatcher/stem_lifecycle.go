package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type StemLifecycleState uint8

const (
	StemPending StemLifecycleState = iota
	StemReady
	StemFrozen
	StemConsuming
	StemReleased
	StemFailed
)

var (
	ErrStemGeneration = errors.New("stem generation mismatch")
	ErrStemRequestID  = errors.New("stem request id mismatch")
	ErrStemTransition = errors.New("invalid stem lifecycle transition")
)

type StemReadyEvent struct {
	RequestID  string
	Generation uint64
	Stem       string
}

type stemLifecycle struct {
	state           StemLifecycleState
	refs            int
	acquired        int
	expectedReaders int
}

// StemLifecycle coordinates post-inference publication. The current Demucs
// protocol returns one final response, so this contract deliberately models
// transfer/analysis wavefronts and does not claim inference-time streaming.
type StemLifecycle struct {
	mu         sync.Mutex
	requestID  string
	generation uint64
	stems      map[string]*stemLifecycle
	ready      chan StemReadyEvent
	cancel     context.CancelCauseFunc
	stop       func() bool
}

func NewStemLifecycle(ctx context.Context, requestID string, generation uint64, stems []string, readersPerStem, readyCapacity int) (*StemLifecycle, context.Context, error) {
	if ctx == nil || requestID == "" || generation == 0 || len(stems) == 0 || readersPerStem <= 0 || readyCapacity <= 0 {
		return nil, nil, ErrStemTransition
	}
	child, cancel := context.WithCancelCause(ctx)
	lifecycle := &StemLifecycle{
		requestID:  requestID,
		generation: generation,
		stems:      make(map[string]*stemLifecycle, len(stems)),
		ready:      make(chan StemReadyEvent, readyCapacity),
		cancel:     cancel,
	}
	for _, stem := range stems {
		if stem == "" {
			cancel(ErrStemTransition)
			return nil, nil, ErrStemTransition
		}
		if _, exists := lifecycle.stems[stem]; exists {
			cancel(ErrStemTransition)
			return nil, nil, fmt.Errorf("%w: duplicate stem %q", ErrStemTransition, stem)
		}
		lifecycle.stems[stem] = &stemLifecycle{state: StemPending, expectedReaders: readersPerStem}
	}
	lifecycle.stop = context.AfterFunc(child, func() { lifecycle.failAll(context.Cause(child)) })
	return lifecycle, child, nil
}

func (l *StemLifecycle) ReadyEvents() <-chan StemReadyEvent { return l.ready }

// Publish validates the daemon envelope and makes a transferred stem visible
// exactly once. The bounded ready queue is deliberately fail-closed: allowing
// an unbounded producer would retain SHM/mmap backing while consumers stall.
func (l *StemLifecycle) Publish(requestID string, generation uint64, stem string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if requestID != l.requestID {
		return ErrStemRequestID
	}
	entry, err := l.entry(generation, stem)
	if err != nil {
		return err
	}
	if entry.state != StemPending {
		return fmt.Errorf("%w: %s is %d", ErrStemTransition, stem, entry.state)
	}
	entry.state = StemFrozen
	select {
	case l.ready <- StemReadyEvent{RequestID: requestID, Generation: generation, Stem: stem}:
		return nil
	default:
		entry.state = StemFailed
		err := fmt.Errorf("%w: ready queue capacity exhausted", ErrStemTransition)
		l.cancel(err)
		return err
	}
}

func (l *StemLifecycle) MarkReady(generation uint64, stem string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.entry(generation, stem)
	if err != nil {
		return err
	}
	if entry.state != StemPending {
		return fmt.Errorf("%w: %s is %d", ErrStemTransition, stem, entry.state)
	}
	entry.state = StemReady
	return nil
}

func (l *StemLifecycle) Freeze(generation uint64, stem string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.entry(generation, stem)
	if err != nil {
		return err
	}
	if entry.state != StemReady {
		return fmt.Errorf("%w: %s cannot freeze from %d", ErrStemTransition, stem, entry.state)
	}
	entry.state = StemFrozen
	select {
	case l.ready <- StemReadyEvent{RequestID: l.requestID, Generation: generation, Stem: stem}:
		return nil
	default:
		entry.state = StemFailed
		err := fmt.Errorf("%w: ready queue capacity exhausted", ErrStemTransition)
		l.cancel(err)
		return err
	}
}

func (l *StemLifecycle) Acquire(generation uint64, stem string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.entry(generation, stem)
	if err != nil {
		return err
	}
	if (entry.state != StemFrozen && entry.state != StemConsuming) || entry.acquired >= entry.expectedReaders {
		return fmt.Errorf("%w: %s is not readable", ErrStemTransition, stem)
	}
	entry.state = StemConsuming
	entry.refs++
	entry.acquired++
	return nil
}

func (l *StemLifecycle) Release(generation uint64, stem string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.entry(generation, stem)
	if err != nil {
		return err
	}
	if entry.state != StemConsuming || entry.refs <= 0 {
		return fmt.Errorf("%w: %s has no reader reference", ErrStemTransition, stem)
	}
	entry.refs--
	if entry.refs == 0 && entry.acquired == entry.expectedReaders {
		entry.state = StemReleased
	}
	return nil
}

func (l *StemLifecycle) Fail(err error) {
	if err == nil {
		err = ErrStemTransition
	}
	l.cancel(err)
}

func (l *StemLifecycle) State(generation uint64, stem string) (StemLifecycleState, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.entry(generation, stem)
	if err != nil {
		return StemFailed, 0, err
	}
	return entry.state, entry.refs, nil
}

func (l *StemLifecycle) Close() {
	if l.stop != nil {
		l.stop()
	}
	l.cancel(context.Canceled)
}

func (l *StemLifecycle) entry(generation uint64, stem string) (*stemLifecycle, error) {
	if generation != l.generation {
		return nil, ErrStemGeneration
	}
	entry, ok := l.stems[stem]
	if !ok {
		return nil, fmt.Errorf("%w: unknown stem %q", ErrStemTransition, stem)
	}
	return entry, nil
}

func (l *StemLifecycle) failAll(cause error) {
	if cause == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.stems {
		if entry.state != StemReleased {
			entry.state = StemFailed
			entry.refs = 0
		}
	}
}
