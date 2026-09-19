package dispatcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"flac_analyzer/orchestrator/metrics"
)

// Mor: DaemonRequest -> DaemonResponse
// Functor: f_demucs ∘ g_ipc
// Semantics: 常駐型 Demucs GPU ワーカーデーモンクライアントおよび接続プール

type DemucsSeparatePayload struct {
	RequestID      string            `json:"request_id"`
	FlacPath       string            `json:"flac_path"`
	ShmTags        map[string]string `json:"shm_tags,omitempty"`
	StorageMode    string            `json:"storage_mode,omitempty"`
	TempDir        string            `json:"temp_dir,omitempty"`
	StartSample    int64             `json:"start_sample"`
	EndSample      int64             `json:"end_sample"`
	UseDml         bool              `json:"use_dml"`
	Generation     uint64            `json:"generation"`
	RequestedStems []string          `json:"requested_stems,omitempty"`
}

type DemucsStemReadyEvent struct {
	Status     string   `json:"status"`
	RequestID  string   `json:"request_id"`
	Generation uint64   `json:"generation"`
	Stem       string   `json:"stem"`
	Info       StemInfo `json:"info"`
	AudioHash  string   `json:"audio_hash"`
	SR         int      `json:"sr"`
}

type DemucsSeparateResponse struct {
	Status    string              `json:"status"`
	AudioHash string              `json:"audio_hash"`
	SR        int                 `json:"sr"`
	Stems     map[string]StemInfo `json:"stems"`
	Profile   map[string]float64  `json:"profile"`
	Message   string              `json:"message,omitempty"`
	Traceback string              `json:"traceback,omitempty"`
}

type DemucsCheckHashPayload struct {
	FlacPath    string `json:"flac_path"`
	StartSample int64  `json:"start_sample"`
	EndSample   int64  `json:"end_sample"`
}

type DemucsCheckHashResponse struct {
	Status    string             `json:"status"`
	AudioHash string             `json:"audio_hash"`
	Profile   map[string]float64 `json:"profile"`
	Message   string             `json:"message,omitempty"`
}

type DemucsDaemonClient struct {
	mu         sync.Mutex
	id         int
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	loggerFunc func(format string, v ...interface{})
	isAlive    bool
	taskCount  int
}

func startDemucsDaemonProcessComplex(id int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) (*DemucsDaemonClient, error) {
	scriptPath := filepath.Join(workingDir, "demucs_daemon.py")
	cmd := exec.Command(pythonPath, scriptPath)
	cmd.Dir = workingDir
	cmd.Env = append(os.Environ(), envVars...)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe for Demucs daemon-%d: %w", id, err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, fmt.Errorf("failed to create stdout pipe for Demucs daemon-%d: %w", id, err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return nil, fmt.Errorf("failed to create stderr pipe for Demucs daemon-%d: %w", id, err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return nil, fmt.Errorf("failed to start Demucs daemon-%d: %w", id, err)
	}

	if cmd.Process != nil {
		_ = AssignPidToJob(cmd.Process.Pid)
	}

	// stderr ストリーミング
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			loggerFunc("[DemucsDaemon-%d] %s", id, scanner.Text())
		}
	}()

	client := &DemucsDaemonClient{
		id:         id,
		cmd:        cmd,
		stdin:      stdinPipe,
		stdout:     bufio.NewReader(stdoutPipe),
		loggerFunc: loggerFunc,
		isAlive:    true,
	}

	// 起動シグナル (Ready) の待機 (モデルロードのため 45秒タイムアウト)
	readyChan := make(chan error, 1)
	go func() {
		line, readErr := client.stdout.ReadString('\n')
		if readErr != nil {
			readyChan <- fmt.Errorf("failed to read ready signal from Demucs daemon: %w", readErr)
			return
		}
		var readyMap map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(line), &readyMap); jsonErr != nil {
			readyChan <- fmt.Errorf("failed to parse ready JSON (%s): %w", line, jsonErr)
			return
		}
		readyChan <- nil
	}()

	select {
	case err := <-readyChan:
		if err != nil {
			_ = client.Close()
			return nil, err
		}
	case <-time.After(45 * time.Second):
		_ = client.Close()
		return nil, fmt.Errorf("timeout waiting for Demucs daemon-%d ready handshake", id)
	}

	return client, nil
}

func (c *DemucsDaemonClient) CheckHash(ctx context.Context, payload DemucsCheckHashPayload) (*DemucsCheckHashResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isAlive {
		return nil, fmt.Errorf("Demucs daemon-%d is not alive", c.id)
	}

	req := map[string]interface{}{
		"command": "check_hash",
		"payload": payload,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal check_hash request: %w", err)
	}

	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		c.isAlive = false
		return nil, fmt.Errorf("failed to send check_hash request to Demucs daemon-%d: %w", c.id, err)
	}

	respChan := make(chan *DemucsCheckHashResponse, 1)
	errChan := make(chan error, 1)

	go func() {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			errChan <- fmt.Errorf("read error from Demucs daemon-%d: %w", c.id, err)
			return
		}
		var resp DemucsCheckHashResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			errChan <- fmt.Errorf("unmarshal error from Demucs daemon-%d (%s): %w", c.id, line, err)
			return
		}
		respChan <- &resp
	}()

	select {
	case <-ctx.Done():
		_ = c.closeLocked()
		return nil, ctx.Err()
	case err := <-errChan:
		_ = c.closeLocked()
		return nil, err
	case resp := <-respChan:
		if resp.Status != "success" {
			return nil, fmt.Errorf("Demucs daemon check_hash failed: %s", resp.Message)
		}
		c.taskCount++
		return resp, nil
	}
}

func (c *DemucsDaemonClient) Separate(ctx context.Context, payload DemucsSeparatePayload) (*DemucsSeparateResponse, error) {
	return c.SeparateWithEvents(ctx, payload, nil)
}

func (c *DemucsDaemonClient) DecodeMix(ctx context.Context, payload DemucsSeparatePayload) (*DemucsSeparateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isAlive {
		return nil, fmt.Errorf("Demucs daemon-%d is not alive", c.id)
	}
	reqBytes, err := json.Marshal(map[string]any{"command": "decode_mix", "payload": payload})
	if err != nil {
		return nil, fmt.Errorf("marshal decode_mix request: %w", err)
	}
	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		_ = c.closeLocked()
		return nil, fmt.Errorf("send decode_mix request to Demucs daemon-%d: %w", c.id, err)
	}
	type result struct {
		response *DemucsSeparateResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		line, readErr := c.stdout.ReadString('\n')
		if readErr != nil {
			resultCh <- result{err: readErr}
			return
		}
		var response DemucsSeparateResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			resultCh <- result{err: err}
			return
		}
		resultCh <- result{response: &response}
	}()
	select {
	case <-ctx.Done():
		_ = c.closeLocked()
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			_ = c.closeLocked()
			return nil, fmt.Errorf("read decode_mix response: %w", result.err)
		}
		if result.response.Status != "success" {
			return nil, fmt.Errorf("decode_mix failed: %s", result.response.Message)
		}
		c.taskCount++
		return result.response, nil
	}
}

func (c *DemucsDaemonClient) SeparateWithEvents(ctx context.Context, payload DemucsSeparatePayload, onReady func(DemucsStemReadyEvent) error) (*DemucsSeparateResponse, error) {
	if payload.RequestID == "" || payload.Generation == 0 {
		return nil, fmt.Errorf("Demucs stem event protocol requires request id and non-zero generation")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isAlive {
		return nil, fmt.Errorf("Demucs daemon-%d is not alive", c.id)
	}

	req := map[string]interface{}{
		"command": "separate",
		"payload": payload,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal separate request: %w", err)
	}

	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		_ = c.closeLocked()
		return nil, fmt.Errorf("failed to send separate request to Demucs daemon-%d: %w", c.id, err)
	}

	respChan := make(chan *DemucsSeparateResponse, 1)
	errChan := make(chan error, 1)

	go func() {
		for {
			line, err := c.stdout.ReadString('\n')
			if err != nil {
				errChan <- fmt.Errorf("read error from Demucs daemon-%d: %w", c.id, err)
				return
			}
			var envelope struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(line), &envelope); err != nil {
				errChan <- fmt.Errorf("unmarshal error from Demucs daemon-%d (%s): %w", c.id, line, err)
				return
			}
			if envelope.Status == "stem_ready" {
				var event DemucsStemReadyEvent
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					errChan <- err
					return
				}
				if err := validateDemucsStemReadyEvent(payload, event); err != nil {
					errChan <- err
					return
				}
				if onReady != nil {
					if err := onReady(event); err != nil {
						errChan <- fmt.Errorf("consume stem-ready event: %w", err)
						return
					}
				}
				continue
			}
			var resp DemucsSeparateResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				errChan <- fmt.Errorf("unmarshal error from Demucs daemon-%d (%s): %w", c.id, line, err)
				return
			}
			respChan <- &resp
			return
		}
	}()

	select {
	case <-ctx.Done():
		_ = c.closeLocked()
		return nil, ctx.Err()
	case err := <-errChan:
		_ = c.closeLocked()
		return nil, err
	case resp := <-respChan:
		if resp.Status != "success" {
			return nil, fmt.Errorf("Demucs daemon separate failed: %s (%s)", resp.Message, resp.Traceback)
		}
		c.taskCount++
		return resp, nil
	}
}

func validateDemucsStemReadyEvent(payload DemucsSeparatePayload, event DemucsStemReadyEvent) error {
	if event.Status != "stem_ready" || event.RequestID == "" || event.RequestID != payload.RequestID {
		return fmt.Errorf("Demucs stem event request id mismatch")
	}
	if event.Generation == 0 || event.Generation != payload.Generation {
		return fmt.Errorf("Demucs stem event generation mismatch")
	}
	if event.Stem == "" || event.AudioHash == "" || event.SR <= 0 {
		return fmt.Errorf("Demucs stem event is missing transfer metadata")
	}
	if len(event.Info.Shape) == 0 || event.Info.Dtype == "" {
		return fmt.Errorf("Demucs stem event %q is missing storage shape or dtype", event.Stem)
	}
	return nil
}

// closeLocked terminates stdin and kills daemon process while caller already holds c.mu.
func (c *DemucsDaemonClient) closeLocked() error {
	if !c.isAlive {
		return nil
	}
	c.isAlive = false
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}

func (c *DemucsDaemonClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

// clientFactory abstracts daemon process creation for testing.
type clientFactory func(id int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) (*DemucsDaemonClient, error)

// defaultClientFactory is the production factory.
var defaultClientFactory clientFactory = startDemucsDaemonProcessComplex

type DemucsDaemonPool struct {
	mu         sync.Mutex
	cond       *sync.Cond
	capacity   int
	clients    []*DemucsDaemonClient // registry: len(clients) <= capacity
	idle       []*DemucsDaemonClient // idle subset; always also in clients
	spawning   bool                  // serialized spawning reservation
	pythonPath string
	workingDir string
	envVars    []string
	loggerFunc func(format string, v ...interface{})
	factory    clientFactory
	isClosed   bool
	nextID     int
}

func NewDemucsDaemonPool(capacity int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) *DemucsDaemonPool {
	return NewDemucsDaemonPoolWithFactory(capacity, pythonPath, workingDir, envVars, loggerFunc, defaultClientFactory)
}

func NewDemucsDaemonPoolWithFactory(capacity int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{}), factory clientFactory) *DemucsDaemonPool {
	if capacity <= 0 {
		capacity = 1
	}
	if capacity > 1 {
		capacity = 1 // Demucs has exactly one dedicated GPU slot.
	}
	p := &DemucsDaemonPool{
		capacity:   capacity,
		clients:    make([]*DemucsDaemonClient, 0, capacity),
		idle:       make([]*DemucsDaemonClient, 0, capacity),
		pythonPath: pythonPath,
		workingDir: workingDir,
		envVars:    envVars,
		loggerFunc: loggerFunc,
		factory:    factory,
		nextID:     1,
	}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *DemucsDaemonPool) Prewarm(ctx context.Context, count int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if count > p.capacity {
		count = p.capacity
	}

	for len(p.clients) < count {
		id := p.nextID
		p.nextID++
		p.loggerFunc("[DemucsDaemonPool] Prewarming DemucsDaemon-%d (VRAM model pre-load)...", id)
		// Unlock during potentially slow process start
		p.mu.Unlock()
		client, err := p.factory(id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
		p.mu.Lock()
		if err != nil {
			return fmt.Errorf("failed to prewarm Demucs daemon-%d: %w", id, err)
		}
		if p.isClosed {
			_ = client.Close()
			return fmt.Errorf("DemucsDaemonPool closed during prewarm")
		}
		p.clients = append(p.clients, client)
		p.idle = append(p.idle, client)
		metrics.AnalyzerDemucsDaemonPoolSize.Set(float64(len(p.clients)))
		p.cond.Broadcast()
	}
	return nil
}

func (p *DemucsDaemonPool) Acquire(ctx context.Context) (*DemucsDaemonClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for {
		if p.isClosed {
			return nil, fmt.Errorf("DemucsDaemonPool is closed")
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// 1. Try to take an idle client
		if len(p.idle) > 0 {
			client := p.idle[len(p.idle)-1]
			p.idle = p.idle[:len(p.idle)-1]

			if client.isAlive {
				return client, nil
			}

			// Dead idle client: kill its process (even if isAlive=false, process may be zombie)
			p.loggerFunc("[DemucsDaemonPool] DemucsDaemon-%d is dead, recycling...", client.id)
			p.killClientProcessLocked(client)

			// Attempt restart, still holding lock briefly for reservation
			id := client.id
			p.removeClientLocked(client)
			metrics.AnalyzerDemucsDaemonPoolSize.Set(float64(len(p.clients)))

			// Spawn replacement outside lock
			p.mu.Unlock()
			newClient, err := p.factory(id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
			p.mu.Lock()

			if err != nil {
				p.loggerFunc("[DemucsDaemonPool] Failed to restart DemucsDaemon-%d: %v, capacity freed", id, err)
				// Capacity is freed (client was removed). Wake waiters so they can try spawning.
				p.cond.Broadcast()
				// Return error but pool is not permanently broken
				return nil, fmt.Errorf("DemucsDaemonPool restart DemucsDaemon-%d failed: %w", id, err)
			}
			if p.isClosed {
				_ = newClient.Close()
				return nil, fmt.Errorf("DemucsDaemonPool closed during restart")
			}
			p.clients = append(p.clients, newClient)
			metrics.AnalyzerDemucsDaemonPoolSize.Set(float64(len(p.clients)))
			return newClient, nil
		}

		// 2. No idle client: try to spawn if capacity available and no other spawning
		if len(p.clients) < p.capacity && !p.spawning {
			p.spawning = true
			id := p.nextID
			p.nextID++

			p.mu.Unlock()
			p.loggerFunc("[DemucsDaemonPool] Scaling up: Spawning new DemucsDaemon-%d...", id)
			client, err := p.factory(id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
			p.mu.Lock()
			p.spawning = false

			if err != nil {
				p.cond.Broadcast() // wake other waiters to retry
				return nil, err
			}
			if p.isClosed {
				_ = client.Close()
				return nil, fmt.Errorf("DemucsDaemonPool closed during spawn")
			}
			p.clients = append(p.clients, client)
			metrics.AnalyzerDemucsDaemonPoolSize.Set(float64(len(p.clients)))
			return client, nil
		}

		// 3. At capacity and all busy: wait for signal with context awareness
		// Use a done channel to integrate ctx cancellation with cond.Wait
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				p.cond.Broadcast() // wake the waiter
			case <-done:
			}
		}()
		p.cond.Wait()
		close(done)
		// Loop back to re-check conditions
	}
}

func (p *DemucsDaemonPool) Release(client *DemucsDaemonClient) {
	if client == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isClosed {
		p.killClientProcessLocked(client)
		return
	}

	// 50 タスクごとに VRAM クリーンアップのためにプロセスをリサイクル
	if client.taskCount >= 50 {
		p.loggerFunc("[DemucsDaemonPool] DemucsDaemon-%d reached task recycling threshold (50 tasks), restarting cleanly...", client.id)
		id := client.id
		p.killClientProcessLocked(client)
		p.removeClientLocked(client)
		metrics.AnalyzerDemucsDaemonPoolSize.Set(float64(len(p.clients)))

		// Attempt restart outside lock
		p.mu.Unlock()
		newClient, err := p.factory(id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
		p.mu.Lock()

		if err != nil {
			p.loggerFunc("[DemucsDaemonPool] Failed to recycle DemucsDaemon-%d: %v, capacity freed for future spawn", id, err)
			// Capacity freed. Wake waiters to allow re-spawn.
			p.cond.Broadcast()
			return
		}
		if p.isClosed {
			_ = newClient.Close()
			return
		}
		p.clients = append(p.clients, newClient)
		p.idle = append(p.idle, newClient)
		metrics.AnalyzerDemucsDaemonPoolSize.Set(float64(len(p.clients)))
		p.cond.Broadcast()
		return
	}

	p.idle = append(p.idle, client)
	p.cond.Broadcast()
}

// killClientProcessLocked ensures the client process is terminated.
// Must be called with p.mu held. Kills process even if isAlive is already false.
func (p *DemucsDaemonPool) killClientProcessLocked(client *DemucsDaemonClient) {
	if client == nil {
		return
	}
	client.isAlive = false
	if client.stdin != nil {
		_ = client.stdin.Close()
	}
	if client.cmd != nil && client.cmd.Process != nil {
		_ = client.cmd.Process.Kill()
		_ = client.cmd.Wait()
	}
}

// removeClientLocked removes a client from the registry. Caller holds p.mu.
func (p *DemucsDaemonPool) removeClientLocked(client *DemucsDaemonClient) {
	for i, c := range p.clients {
		if c == client {
			p.clients = append(p.clients[:i], p.clients[i+1:]...)
			return
		}
	}
}

func (p *DemucsDaemonPool) replaceClientLocked(oldClient, newClient *DemucsDaemonClient) bool {
	for i, client := range p.clients {
		if client == oldClient {
			p.clients[i] = newClient
			return true
		}
	}
	return false
}

func (p *DemucsDaemonPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isClosed = true
	// Kill all clients
	for _, c := range p.clients {
		p.killClientProcessLocked(c)
	}
	p.clients = nil
	p.idle = nil
	metrics.AnalyzerDemucsDaemonPoolSize.Set(0)
	p.cond.Broadcast() // wake all waiters so they observe isClosed
	return nil
}
