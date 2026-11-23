// Mor: Context -> WorkerDaemonClient
// Functor: f_pool ∘ g_spawn
// Semantics: Category: Thread-Safe Worker Daemon Connection Pool
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// WorkerDaemonPool manages a thread-safe connection pool of persistent worker_daemon.py clients.
type WorkerDaemonPool struct {
	maxDaemons    int
	spawningCount int
	pythonPath    string
	parentDir     string
	env           []string
	logger        func(format string, v ...interface{})
	role          WorkerDaemonRole
	daemons       chan *WorkerDaemonClient
	allDaemons    []*WorkerDaemonClient
	retiring      map[*WorkerDaemonClient]chan struct{}
	capNotify     chan struct{}
	closedCh      chan struct{}
	mu            sync.Mutex
	closed        bool
	closeChanOnce sync.Once
	spawnCond     *sync.Cond
	nextID          int
	hasRetainPolicy bool
	retainIdle      bool
	closeHook       func(*WorkerDaemonClient) error
	spawnHook       func(int) (*WorkerDaemonClient, error)

	logMu      sync.Mutex
	logRunning bool
	logOrder   []string
	logLatest  map[string]poolLogEntry
}

type poolLogEntry struct {
	format string
	args   []interface{}
}

const maxPendingPoolLogKeys = 128

// SetRetainIdle configures and atomically updates the idle-retention policy under p.mu,
// serializing policy switches with ReleaseChecked, TrimIdleWithWatchers, and AcquireTracked.
func (p *WorkerDaemonPool) SetRetainIdle(retain bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.hasRetainPolicy = true
	p.retainIdle = retain
	p.mu.Unlock()
}

func (p *WorkerDaemonPool) shouldRetainIdleLocked() bool {
	if !p.hasRetainPolicy {
		return true
	}
	return p.retainIdle
}

func (p *WorkerDaemonPool) activeRetainPolicyBlocksTrimLocked() bool {
	return p.hasRetainPolicy && p.retainIdle
}

func (p *WorkerDaemonPool) ensureClosedChLocked() chan struct{} {
	if p.closedCh == nil {
		p.closedCh = make(chan struct{})
	}
	return p.closedCh
}

func (p *WorkerDaemonPool) ensureCapNotifyLocked() chan struct{} {
	if p.capNotify == nil {
		p.capNotify = make(chan struct{})
	}
	return p.capNotify
}

func (p *WorkerDaemonPool) broadcastCapacityLocked() {
	if p.capNotify != nil {
		close(p.capNotify)
		p.capNotify = make(chan struct{})
	}
}

func (p *WorkerDaemonPool) ensureSpawnCondLocked() {
	if p.spawnCond == nil {
		p.spawnCond = sync.NewCond(&p.mu)
	}
}

func (p *WorkerDaemonPool) spawnWorkerClient(id int, startGate func(func() error) error) (*WorkerDaemonClient, error) {
	if p != nil && p.spawnHook != nil {
		if startGate != nil {
			if err := startGate(func() error { return nil }); err != nil {
				return nil, err
			}
		}
		return p.spawnHook(id)
	}
	return NewWorkerDaemonClientWithGate(id, p.pythonPath, p.parentDir, p.env, p.asyncLogger(fmt.Sprintf("client-%d", id)), p.role, startGate)
}

// asyncLogger returns a logger that never blocks callers: messages go through the bounded,
// key-coalescing queue so a stalled sink cannot hold spawn/retirement fences.
func (p *WorkerDaemonPool) asyncLogger(keyPrefix string) func(string, ...interface{}) {
	if p == nil {
		return nil
	}
	return func(format string, args ...interface{}) {
		// Key by daemon/diagnostic format (not the rendered text) so changing arguments
		// coalesce into one latest payload instead of filling the bounded queue.
		p.logPoolErrorAsync(keyPrefix+"|"+format, format, args...)
	}
}

func (p *WorkerDaemonPool) closeClientForRetirement(client *WorkerDaemonClient) error {
	if client == nil {
		return nil
	}
	if p != nil && p.closeHook != nil {
		return p.closeHook(client)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return client.CloseForRetirement(ctx)
}

func (p *WorkerDaemonPool) attachLatePipeDrainer(client *WorkerDaemonClient) {
	if client == nil {
		return
	}
	_ = client.AttachLateTeardownLogger(p.asyncLogger(fmt.Sprintf("late-%d", client.id)))
}

func (p *WorkerDaemonPool) logPoolError(format string, args ...interface{}) {
	if p != nil && p.logger != nil {
		p.logger(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (p *WorkerDaemonPool) logPoolErrorAsync(coalesceKey, format string, args ...interface{}) {
	if p == nil {
		return
	}
	p.logMu.Lock()
	if p.logLatest == nil {
		p.logLatest = make(map[string]poolLogEntry)
	}
	if _, exists := p.logLatest[coalesceKey]; !exists {
		if len(p.logOrder) >= maxPendingPoolLogKeys {
			evict := p.logOrder[0]
			p.logOrder = p.logOrder[1:]
			delete(p.logLatest, evict)
		}
		p.logOrder = append(p.logOrder, coalesceKey)
	}
	p.logLatest[coalesceKey] = poolLogEntry{format: format, args: args}
	startWorker := !p.logRunning
	if startWorker {
		p.logRunning = true
	}
	p.logMu.Unlock()
	if startWorker {
		go p.drainPoolLogs()
	}
}

func (p *WorkerDaemonPool) drainPoolLogs() {
	for {
		p.logMu.Lock()
		if len(p.logOrder) == 0 {
			p.logRunning = false
			p.logMu.Unlock()
			return
		}
		key := p.logOrder[0]
		p.logOrder = p.logOrder[1:]
		entry := p.logLatest[key]
		delete(p.logLatest, key)
		p.logMu.Unlock()

		p.logPoolError(entry.format, entry.args...)
	}
}

func (p *WorkerDaemonPool) completeClientRetirementLocked(client *WorkerDaemonClient) {
	p.removeDaemonLocked(client)
	if ch, ok := p.retiring[client]; ok {
		close(ch)
		delete(p.retiring, client)
	}
}

func (p *WorkerDaemonPool) clientMayStillBeRunning(client *WorkerDaemonClient, closeErr error) bool {
	if closeErr == nil || client == nil {
		return false
	}
	if p != nil && p.closeHook != nil {
		// Synthetic test closeHook simulates exit progression directly via hook return errors.
		return true
	}
	return client.HasLiveProcess()
}

// NewWorkerDaemonPool creates a new pool with the specified maximum capacity.
func NewWorkerDaemonPool(
	maxDaemons int,
	pythonPath string,
	parentDir string,
	env []string,
	logger func(format string, v ...interface{}),
	role WorkerDaemonRole,
) *WorkerDaemonPool {
	if maxDaemons <= 0 {
		maxDaemons = 2
	}
	p := &WorkerDaemonPool{
		maxDaemons:    maxDaemons,
		spawningCount: 0,
		pythonPath:    pythonPath,
		parentDir:     parentDir,
		env:           env,
		logger:        logger,
		role:          role,
		daemons:       make(chan *WorkerDaemonClient, maxDaemons),
		allDaemons:    make([]*WorkerDaemonClient, 0, maxDaemons),
		nextID:        1,
	}
	p.spawnCond = sync.NewCond(&p.mu)
	return p
}

// Prewarm warms up count daemons at startup.
func (p *WorkerDaemonPool) Prewarm(ctx context.Context, count int) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("worker daemon pool is closed")
	}
	target := count
	if target > p.maxDaemons {
		target = p.maxDaemons
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < target; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := p.spawnSlot(ctx)
			if err != nil {
				if p.logger != nil {
					p.logPoolErrorAsync("prewarm-spawn-note", "[DaemonPool] Prewarm spawn note: %v", err)
				}
				return
			}
			p.Release(client)
		}()
	}
	wg.Wait()
	return nil
}

// Acquire retrieves a healthy daemon from the pool, spawning a new one if necessary.
func (p *WorkerDaemonPool) Acquire(ctx context.Context) (*WorkerDaemonClient, error) {
	client, _, err := p.AcquireTracked(ctx)
	return client, err
}

// AcquireTracked works like Acquire but also returns a non-nil done channel when a cancelled
// spawn/retirement continues asynchronously, allowing callers (such as GPUArbiter) to fence
// until any late child startup failure or exit completes.
func (p *WorkerDaemonPool) finalizeIdleCheckout(ctx context.Context, client *WorkerDaemonClient) (*WorkerDaemonClient, bool, <-chan struct{}, error) {
	p.mu.Lock()
	closedNow := p.closed
	ctxErr := ctx.Err()
	retainIdle := p.shouldRetainIdleLocked()
	healthy := !closedNow && ctxErr == nil && client.IsHealthy()
	p.mu.Unlock()
	if healthy {
		return client, false, nil, nil
	}
	if closedNow {
		if err := p.Discard(client); err != nil {
			return nil, false, p.RetireWatcher(client), fmt.Errorf("worker daemon pool closed during acquire (retire error: %w)", err)
		}
		return nil, false, nil, fmt.Errorf("worker daemon pool is closed")
	}
	if ctxErr != nil {
		if !retainIdle {
			if retireErr := p.Discard(client); retireErr != nil {
				return nil, false, p.RetireWatcher(client), fmt.Errorf("acquire cancelled (%w); retire error: %w", ctxErr, retireErr)
			}
			return nil, false, nil, fmt.Errorf("timeout waiting for idle worker daemon: %w", ctxErr)
		}
		if retireErr := p.ReleaseChecked(client); retireErr != nil {
			return nil, false, p.RetireWatcher(client), fmt.Errorf("acquire cancelled (%w); retire error: %w", ctxErr, retireErr)
		}
		return nil, false, nil, fmt.Errorf("timeout waiting for idle worker daemon: %w", ctxErr)
	}
	if err := p.Discard(client); err != nil {
		return nil, false, p.RetireWatcher(client), fmt.Errorf("retire unhealthy worker daemon %d: %w", client.id, err)
	}
	return nil, true, nil, nil
}

// AcquireTracked checks out or spawns a healthy daemon, consulting the pool's current
// retention policy atomically under p.mu on cancellation.
func (p *WorkerDaemonPool) AcquireTracked(ctx context.Context) (*WorkerDaemonClient, <-chan struct{}, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, nil, fmt.Errorf("worker daemon pool is closed")
		}
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, nil, fmt.Errorf("acquire cancelled: %w", err)
		}

		// Case 1: An idle daemon is available in the channel
		select {
		case client := <-p.daemons:
			p.mu.Unlock()
			c, retry, retireCh, err := p.finalizeIdleCheckout(ctx, client)
			if retry {
				continue
			}
			return c, retireCh, err
		default:
		}

		// Case 2: Pool capacity is not reached yet (including currently spawning daemons)
		if len(p.allDaemons)+p.spawningCount < p.maxDaemons {
			// Reserve slot under lock to prevent thundering herd
			p.spawningCount++
			id := p.nextID
			p.nextID++
			closedCh := p.ensureClosedChLocked()
			p.mu.Unlock()

			client, spawnDone, err := p.doSpawnComplex(ctx, id, closedCh)
			return client, spawnDone, err
		}
		capCh := p.ensureCapNotifyLocked()
		p.mu.Unlock()

		// Case 3: Pool is full or slots are reserved, wait for idle daemon, capacity recovery, or cancellation
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("timeout waiting for idle worker daemon: %w", ctx.Err())
		case <-capCh:
			continue
		case client, ok := <-p.daemons:
			if !ok {
				return nil, nil, fmt.Errorf("worker daemon pool channel closed")
			}
			c, retry, retireCh, err := p.finalizeIdleCheckout(ctx, client)
			if retry {
				continue
			}
			return c, retireCh, err
		}
	}
}

func (p *WorkerDaemonPool) spawnSlot(ctx context.Context) (*WorkerDaemonClient, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("worker daemon pool is closed")
	}
	if len(p.allDaemons)+p.spawningCount >= p.maxDaemons {
		p.mu.Unlock()
		return nil, fmt.Errorf("pool capacity reached")
	}
	p.spawningCount++
	id := p.nextID
	p.nextID++
	closedCh := p.ensureClosedChLocked()
	p.mu.Unlock()

	client, _, err := p.doSpawnComplex(ctx, id, closedCh)
	return client, err
}

func (p *WorkerDaemonPool) doSpawnComplex(ctx context.Context, id int, closedCh <-chan struct{}) (*WorkerDaemonClient, <-chan struct{}, error) {
	if p.logger != nil {
		p.logPoolErrorAsync(fmt.Sprintf("spawn-start-%d", id), "[DaemonPool] Spawning new WorkerDaemon-%d...", id)
	}

	type spawnResult struct {
		client *WorkerDaemonClient
		err    error
	}
	type spawnHandoff struct {
		abandoned bool
		completed bool
	}
	handoff := &spawnHandoff{}
	resCh := make(chan spawnResult, 1)
	spawnDone := make(chan struct{})
	publishOutcome := func(res spawnResult, abandonedMsg string) {
		p.mu.Lock()
		abandoned := handoff.abandoned
		if !abandoned {
			handoff.completed = true
		}
		p.mu.Unlock()
		if abandoned {
			if abandonedMsg != "" {
				p.logPoolErrorAsync(fmt.Sprintf("spawn-abandoned-%d", id), "%s", abandonedMsg)
			}
			return
		}
		resCh <- res
	}

	go func() {
		var watchCh <-chan struct{}
		defer func() {
			p.mu.Lock()
			p.spawningCount--
			p.broadcastCapacityLocked()
			p.ensureSpawnCondLocked()
			p.spawnCond.Broadcast()
			p.mu.Unlock()
			if watchCh != nil {
				<-watchCh
			}
			close(spawnDone)
		}()

		startGate := func(startChild func() error) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.closed {
				return fmt.Errorf("worker daemon pool is closed")
			}
			if handoff.abandoned {
				return fmt.Errorf("spawn abandoned before process start")
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("context cancelled before spawning daemon %d: %w", id, err)
			}
			return startChild()
		}

		c, err := p.spawnWorkerClient(id, startGate)
		if err != nil {
			if c != nil {
				// Preserve ownership of any partially started live child on startup failure.
				p.mu.Lock()
				p.allDaemons = append(p.allDaemons, c)
				p.mu.Unlock()
				if retireErr := p.Discard(c); retireErr != nil {
					watchCh = p.RetireWatcher(c)
					err = errors.Join(err, fmt.Errorf("retire failed spawn daemon %d: %w", id, retireErr))
				}
				publishOutcome(
					spawnResult{client: c, err: err},
					fmt.Sprintf("[DaemonPool] Abandoned daemon-%d spawn failure/rollback (watcher preserved=%v): %v", id, watchCh != nil, err),
				)
				return
			}
			publishOutcome(
				spawnResult{err: err},
				fmt.Sprintf("[DaemonPool] Abandoned daemon-%d spawn failed: %v", id, err),
			)
			return
		}
		if c == nil {
			nilErr := fmt.Errorf("spawn worker daemon %d returned nil client", id)
			publishOutcome(
				spawnResult{err: nilErr},
				fmt.Sprintf("[DaemonPool] %v", nilErr),
			)
			return
		}

		p.mu.Lock()
		if handoff.abandoned || p.closed {
			p.allDaemons = append(p.allDaemons, c)
			p.mu.Unlock()
			retireErr := p.Discard(c)
			if retireErr != nil {
				watchCh = p.RetireWatcher(c)
			}
			if retireErr != nil {
				publishOutcome(
					spawnResult{client: c, err: fmt.Errorf("pool closed during spawn (retire error: %w)", retireErr)},
					fmt.Sprintf("[DaemonPool] Abandoned daemon-%d post-spawn retire failed (watching exit): %v", id, retireErr),
				)
			} else {
				publishOutcome(
					spawnResult{err: fmt.Errorf("pool closed during spawn")},
					"",
				)
			}
			return
		}
		p.allDaemons = append(p.allDaemons, c)
		handoff.completed = true
		p.mu.Unlock()
		resCh <- spawnResult{client: c}
	}()

	abandonSpawn := func(reason error, wrapFmt string) (*WorkerDaemonClient, <-chan struct{}, error) {
		p.mu.Lock()
		completed := handoff.completed
		if !completed {
			handoff.abandoned = true
		}
		p.mu.Unlock()
		if completed {
			// Constructor already committed an outcome to resCh; consume it so errors/watchers are never lost.
			res := <-resCh
			if res.err != nil {
				if p.logger != nil {
					p.logPoolErrorAsync(fmt.Sprintf("spawn-cancel-%d", id), "[DaemonPool] Spawn daemon-%d cancelled alongside published failure: %v", id, res.err)
				}
				var watchCh <-chan struct{}
				if res.client != nil {
					watchCh = p.RetireWatcher(res.client)
				}
				return nil, watchCh, errors.Join(fmt.Errorf("%s: %w", wrapFmt, reason), res.err)
			}
			if res.client != nil {
				if retireErr := p.Discard(res.client); retireErr != nil {
					return nil, p.RetireWatcher(res.client), errors.Join(fmt.Errorf("%s: %w", wrapFmt, reason), fmt.Errorf("retire cancelled spawn daemon %d: %w", id, retireErr))
				}
			}
			return nil, nil, fmt.Errorf("%s: %w", wrapFmt, reason)
		}
		return nil, spawnDone, fmt.Errorf("%s: %w", wrapFmt, reason)
	}

	select {
	case <-ctx.Done():
		return abandonSpawn(ctx.Err(), fmt.Sprintf("context cancelled while spawning daemon %d", id))
	case <-closedCh:
		return abandonSpawn(fmt.Errorf("worker daemon pool is closed"), fmt.Sprintf("pool closed while spawning daemon %d", id))
	case res := <-resCh:
		if res.err != nil {
			var watchCh <-chan struct{}
			if res.client != nil {
				watchCh = p.RetireWatcher(res.client)
			}
			return nil, watchCh, res.err
		}
		// Finalize checkout under p.mu so concurrent cancellation or Pool.Close() never loses ownership.
		p.mu.Lock()
		closedNow := p.closed
		ctxErr := ctx.Err()
		commitOK := !closedNow && ctxErr == nil
		p.mu.Unlock()
		if commitOK {
			return res.client, nil, nil
		}
		retireErr := p.Discard(res.client)
		var baseErr error
		if closedNow {
			baseErr = fmt.Errorf("pool closed during spawn")
		} else {
			baseErr = fmt.Errorf("context cancelled while spawning daemon %d: %w", id, ctxErr)
		}
		if retireErr != nil {
			return nil, p.RetireWatcher(res.client), errors.Join(baseErr, fmt.Errorf("retire daemon %d: %w", id, retireErr))
		}
		return nil, nil, baseErr
	}
}

// Release returns a worker daemon back to the pool, keeping shutdown ownership
// in allDaemons until Close() confirms process exit.
func (p *WorkerDaemonPool) Release(client *WorkerDaemonClient) {
	if err := p.ReleaseChecked(client); err != nil && p.logger != nil {
		p.logPoolErrorAsync(fmt.Sprintf("release-%d", client.id), "[DaemonPool] Failed to retire daemon-%d on release (retaining for shutdown cleanup): %v", client.id, err)
	}
}

// ReleaseChecked returns a healthy client to the idle queue, or retires unhealthy/excess/closed
// clients while surfacing any retirement/exit-confirmation error to the caller.
func (p *WorkerDaemonPool) ReleaseChecked(client *WorkerDaemonClient) error {
	if client == nil {
		return nil
	}

	p.mu.Lock()
	if p.closed || !p.shouldRetainIdleLocked() || !client.IsHealthy() {
		p.mu.Unlock()
		return p.Discard(client)
	}

	select {
	case p.daemons <- client:
		p.mu.Unlock()
		return nil
	default:
		// Channel full, retire excess daemon outside lock.
		p.mu.Unlock()
		return p.Discard(client)
	}
}

// Discard permanently retires client from the idle pool and closes its underlying process.
// Shutdown ownership in allDaemons is retained until OS exit is confirmed; pipe teardown
// continues asynchronously without stalling pool capacity or GPU fences after child reap.
func (p *WorkerDaemonPool) Discard(client *WorkerDaemonClient) error {
	if client == nil {
		return nil
	}
	closeErr := p.closeClientForRetirement(client)
	if p.clientMayStillBeRunning(client, closeErr) {
		_ = p.RetireWatcher(client)
		return closeErr
	}
	p.attachLatePipeDrainer(client)
	p.mu.Lock()
	p.completeClientRetirementLocked(client)
	p.mu.Unlock()
	return closeErr
}

// DeferRetire initiates retirement of client and returns any pending exit-watcher channel
// alongside the initial close/retirement error so callers can keep GPU/SHM fences active until exit.
func (p *WorkerDaemonPool) DeferRetire(client *WorkerDaemonClient) (<-chan struct{}, error) {
	if p == nil || client == nil {
		return nil, nil
	}
	err := p.Discard(client)
	return p.RetireWatcher(client), err
}

// RetireWatcher atomically checks pool membership under p.mu and returns a pool-owned
// channel that closes ONLY after client exit is confirmed and client is removed from allDaemons.
// Returns nil if client is already removed.
func (p *WorkerDaemonPool) RetireWatcher(client *WorkerDaemonClient) <-chan struct{} {
	if p == nil || client == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inPool := false
	for _, c := range p.allDaemons {
		if c == client {
			inPool = true
			break
		}
	}
	if !inPool {
		return nil
	}
	return p.ensureExitWatcherLocked(client)
}

func (p *WorkerDaemonPool) ensureExitWatcherLocked(client *WorkerDaemonClient) chan struct{} {
	if p.retiring == nil {
		p.retiring = make(map[*WorkerDaemonClient]chan struct{})
	}
	if ch, exists := p.retiring[client]; exists {
		return ch
	}
	done := make(chan struct{})
	p.retiring[client] = done

	go func() {
		confirmRemoved := func() {
			p.attachLatePipeDrainer(client)
			p.mu.Lock()
			p.completeClientRetirementLocked(client)
			p.mu.Unlock()
		}

		tryRetire := func() bool {
			if p.closeHook == nil && !client.HasLiveProcess() {
				if err := p.closeClientForRetirement(client); err != nil {
					p.logPoolErrorAsync(fmt.Sprintf("retired-%d", client.id), "[DaemonPool] Retired daemon-%d exited with cleanup error: %v", client.id, err)
				}
				confirmRemoved()
				return true
			}
			err := p.closeClientForRetirement(client)
			if !p.clientMayStillBeRunning(client, err) {
				if err != nil {
					p.logPoolErrorAsync(fmt.Sprintf("retired-%d", client.id), "[DaemonPool] Retired daemon-%d exited with cleanup error: %v", client.id, err)
				}
				confirmRemoved()
				return true
			}
			if err != nil {
				p.logPoolErrorAsync(fmt.Sprintf("retire-retry-%d", client.id), "[DaemonPool] Retirement retry error while daemon-%d still live: %v", client.id, err)
			}
			return false
		}

		// Bounded active retries first so transient failures recover fast without busy spinning.
		const maxPoolWatchAttempts = 5
		for attempt := 0; attempt < maxPoolWatchAttempts; attempt++ {
			if tryRetire() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Passive wait loop with bounded retry interval so a transiently failing Kill can retry.
		var lastSig <-chan struct{}
		for {
			if sig := client.ExitSignal(); sig != nil && sig != lastSig {
				lastSig = sig
				select {
				case <-sig:
				case <-time.After(500 * time.Millisecond):
				}
			} else {
				time.Sleep(250 * time.Millisecond)
			}
			if tryRetire() {
				return
			}
		}
	}()
	return done
}

func (p *WorkerDaemonPool) removeDaemon(client *WorkerDaemonClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removeDaemonLocked(client)
}

func (p *WorkerDaemonPool) removeDaemonLocked(client *WorkerDaemonClient) {
	for i, c := range p.allDaemons {
		if c == client {
			p.allDaemons = append(p.allDaemons[:i], p.allDaemons[i+1:]...)
			p.broadcastCapacityLocked()
			break
		}
	}
}

// TrimIdle safely closes and removes idle worker daemons from the pool.
// Checked-out (healthy/working) daemons are skipped.
func (p *WorkerDaemonPool) TrimIdle(minRetain int) int {
	trimmed, _ := p.TrimIdleWithWatchers(minRetain)
	return trimmed
}

// TrimIdleWithWatchers closes idle worker daemons beyond minRetain and returns both
// the count retired immediately and any pool exit-watcher channels for unresolved discards.
func (p *WorkerDaemonPool) TrimIdleWithWatchers(minRetain int) (int, []<-chan struct{}) {
	var toClose []*WorkerDaemonClient

	p.mu.Lock()
	if p.closed || p.activeRetainPolicyBlocksTrimLocked() {
		p.mu.Unlock()
		return 0, nil
	}

	for len(p.daemons) > minRetain {
		select {
		case client := <-p.daemons:
			toClose = append(toClose, client)
		default:
			goto Done
		}
	}
Done:
	p.mu.Unlock()

	trimmed := 0
	var watchers []<-chan struct{}
	for _, client := range toClose {
		if w, err := p.DeferRetire(client); err != nil || w != nil {
			if w != nil {
				watchers = append(watchers, w)
			}
			if err != nil {
				p.logPoolErrorAsync(fmt.Sprintf("trim-idle-%d", client.id), "[DaemonPool] Failed to trim idle daemon-%d (watching exit for shutdown/fence cleanup): %v", client.id, err)
			}
			continue
		}
		trimmed++
	}
	return trimmed, watchers
}

// Close stops acquisitions, drains or bounds pending spawns, and closes all tracked worker daemons,
// retaining unresolved clients in allDaemons so subsequent Close() calls can retry.
func (p *WorkerDaemonPool) Close() error {
	p.mu.Lock()
	p.closed = true
	closedCh := p.ensureClosedChLocked()
	p.closeChanOnce.Do(func() {
		close(p.daemons)
		close(closedCh)
	})
	p.ensureSpawnCondLocked()

	shutdownDeadline := time.Now().Add(5 * time.Second)
	stopTimer := time.AfterFunc(time.Until(shutdownDeadline), func() {
		p.mu.Lock()
		p.ensureSpawnCondLocked()
		p.spawnCond.Broadcast()
		p.mu.Unlock()
	})
	for p.spawningCount > 0 && time.Now().Before(shutdownDeadline) {
		p.spawnCond.Wait()
	}
	stopTimer.Stop()
	remainingSpawns := p.spawningCount
	daemons := append([]*WorkerDaemonClient(nil), p.allDaemons...)
	p.mu.Unlock()

	var closeErrs []error
	if remainingSpawns > 0 {
		closeErrs = append(closeErrs, fmt.Errorf("timed out waiting for %d pending worker daemon spawns", remainingSpawns))
	}
	if len(daemons) == 0 {
		return errors.Join(closeErrs...)
	}

	errCh := make(chan error, len(daemons))
	var wg sync.WaitGroup
	for _, c := range daemons {
		if c == nil {
			p.removeDaemon(c)
			continue
		}
		wg.Add(1)
		go func(client *WorkerDaemonClient) {
			defer wg.Done()
			if err := p.Discard(client); err != nil {
				errCh <- fmt.Errorf("close worker daemon %d: %w", client.id, err)
			}
		}(c)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(errCh)
		close(done)
	}()

	remBudget := time.Until(shutdownDeadline)
	if remBudget < 50*time.Millisecond {
		remBudget = 50 * time.Millisecond
	}
	timedOut := false
	select {
	case <-done:
	case <-time.After(remBudget):
		timedOut = true
	}

	for collecting := true; collecting; {
		select {
		case err, ok := <-errCh:
			if !ok {
				collecting = false
			} else if err != nil {
				closeErrs = append(closeErrs, err)
			}
		default:
			collecting = false
		}
	}
	if timedOut {
		closeErrs = append(closeErrs, fmt.Errorf("timed out waiting for worker daemon pool shutdown (%d daemons)", len(daemons)))
		go func() {
			// Drain errCh immediately in background as remaining Discard calls finish; wg.Wait() closes errCh.
			for lateErr := range errCh {
				if lateErr != nil {
					p.logPoolErrorAsync("close-late-retirement", "[DaemonPool] Late retirement error after Close() timeout: %v", lateErr)
				}
			}
		}()
	}
	return errors.Join(closeErrs...)
}

// IsClosed reports whether the pool has been closed for new checkouts.
func (p *WorkerDaemonPool) IsClosed() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	return closed
}
