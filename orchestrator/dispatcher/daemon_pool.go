package dispatcher

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// WorkerDaemonPool manages a thread-safe connection pool of persistent worker_daemon.py clients.
type WorkerDaemonPool struct {
	maxDaemons int
	pythonPath string
	parentDir  string
	env        []string
	logger     func(format string, v ...interface{})
	daemons    chan *WorkerDaemonClient
	allDaemons []*WorkerDaemonClient
	mu         sync.Mutex
	closed     bool
	nextID     int
}

// NewWorkerDaemonPool creates a new pool with the specified maximum capacity.
func NewWorkerDaemonPool(
	maxDaemons int,
	pythonPath string,
	parentDir string,
	env []string,
	logger func(format string, v ...interface{}),
) *WorkerDaemonPool {
	if maxDaemons <= 0 {
		maxDaemons = 2
	}
	return &WorkerDaemonPool{
		maxDaemons: maxDaemons,
		pythonPath: pythonPath,
		parentDir:  parentDir,
		env:        env,
		logger:     logger,
		daemons:    make(chan *WorkerDaemonClient, maxDaemons),
		allDaemons: make([]*WorkerDaemonClient, 0, maxDaemons),
		nextID:     1,
	}
}

// Acquire retrieves a healthy daemon from the pool, spawning a new one if necessary.
func (p *WorkerDaemonPool) Acquire(ctx context.Context) (*WorkerDaemonClient, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("worker daemon pool is closed")
	}

	// Case 1: An idle daemon is available in the channel
	select {
	case client := <-p.daemons:
		p.mu.Unlock()
		if client.IsHealthy() {
			return client, nil
		}
		// Retired/dead daemon: recycle it
		_ = client.Close()
		p.removeDaemon(client)
		return p.spawnNew(ctx)
	default:
	}

	// Case 2: Pool capacity is not reached yet
	if len(p.allDaemons) < p.maxDaemons {
		p.mu.Unlock()
		return p.spawnNew(ctx)
	}
	p.mu.Unlock()

	// Case 3: Pool is full, wait for an idle daemon or context cancellation
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout waiting for idle worker daemon: %w", ctx.Err())
	case client, ok := <-p.daemons:
		if !ok {
			return nil, fmt.Errorf("worker daemon pool channel closed")
		}
		if client.IsHealthy() {
			return client, nil
		}
		_ = client.Close()
		p.removeDaemon(client)
		return p.spawnNew(ctx)
	}
}

func (p *WorkerDaemonPool) spawnNew(ctx context.Context) (*WorkerDaemonClient, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("worker daemon pool is closed")
	}
	id := p.nextID
	p.nextID++
	p.mu.Unlock()

	if p.logger != nil {
		p.logger("[DaemonPool] Spawning new WorkerDaemon-%d...", id)
	}

	// Spawn with context-based cancellation
	type spawnResult struct {
		client *WorkerDaemonClient
		err    error
	}

	resCh := make(chan spawnResult, 1)
	go func() {
		c, err := NewWorkerDaemonClient(id, p.pythonPath, p.parentDir, p.env, p.logger)
		resCh <- spawnResult{client: c, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled while spawning daemon %d: %w", id, ctx.Err())
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		p.mu.Lock()
		p.allDaemons = append(p.allDaemons, res.client)
		p.mu.Unlock()
		return res.client, nil
	}
}

// Release returns a worker daemon back to the pool.
func (p *WorkerDaemonPool) Release(client *WorkerDaemonClient) {
	if client == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || !client.IsHealthy() {
		_ = client.Close()
		p.removeDaemonLocked(client)
		return
	}

	select {
	case p.daemons <- client:
	default:
		// Channel full, close excess daemon
		_ = client.Close()
		p.removeDaemonLocked(client)
	}
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
			break
		}
	}
}

// Close closes all running worker daemons and releases all resources.
func (p *WorkerDaemonPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.daemons)

	daemons := make([]*WorkerDaemonClient, len(p.allDaemons))
	copy(daemons, p.allDaemons)
	p.allDaemons = nil
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, d := range daemons {
		if d != nil {
			wg.Add(1)
			go func(client *WorkerDaemonClient) {
				defer wg.Done()
				_ = client.Close()
			}(d)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	return nil
}
