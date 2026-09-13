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

type DemucsDaemonPool struct {
	mu         sync.Mutex
	capacity   int
	clients    []*DemucsDaemonClient
	idle       chan *DemucsDaemonClient
	pythonPath string
	workingDir string
	envVars    []string
	loggerFunc func(format string, v ...interface{})
	isClosed   bool
	nextID     int
}

// replaceClientLocked keeps the registry bounded when a recycled client is
// replaced. Caller holds p.mu and the old client is not present in idle.
func (p *DemucsDaemonPool) replaceClientLocked(oldClient, newClient *DemucsDaemonClient) bool {
	for i, client := range p.clients {
		if client == oldClient {
			p.clients[i] = newClient
			return true
		}
	}
	return false
}

func NewDemucsDaemonPool(capacity int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) *DemucsDaemonPool {
	if capacity <= 0 {
		capacity = 1
	}
	if capacity > 1 {
		capacity = 1 // Demucs has exactly one dedicated GPU slot.
	}
	return &DemucsDaemonPool{
		capacity:   capacity,
		clients:    make([]*DemucsDaemonClient, 0, capacity),
		idle:       make(chan *DemucsDaemonClient, capacity),
		pythonPath: pythonPath,
		workingDir: workingDir,
		envVars:    envVars,
		loggerFunc: loggerFunc,
		nextID:     1,
	}
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
		client, err := startDemucsDaemonProcessComplex(id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
		if err != nil {
			return fmt.Errorf("failed to prewarm Demucs daemon-%d: %w", id, err)
		}
		p.clients = append(p.clients, client)
		p.idle <- client
	}
	return nil
}

func (p *DemucsDaemonPool) Acquire(ctx context.Context) (*DemucsDaemonClient, error) {
	p.mu.Lock()
	if p.isClosed {
		p.mu.Unlock()
		return nil, fmt.Errorf("DemucsDaemonPool is closed")
	}

	// アイドルデーモンが存在せず、かつ容量に空きがある場合は新規生成
	if len(p.idle) == 0 && len(p.clients) < p.capacity {
		id := p.nextID
		p.nextID++
		p.mu.Unlock()

		p.loggerFunc("[DemucsDaemonPool] Scaling up: Spawning new DemucsDaemon-%d...", id)
		client, err := startDemucsDaemonProcessComplex(id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
		if err != nil {
			return nil, err
		}

		p.mu.Lock()
		p.clients = append(p.clients, client)
		p.mu.Unlock()
		return client, nil
	}
	p.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case client := <-p.idle:
		if !client.isAlive {
			p.loggerFunc("[DemucsDaemonPool] DemucsDaemon-%d is dead, restarting...", client.id)
			_ = client.Close()
			newClient, err := startDemucsDaemonProcessComplex(client.id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
			if err != nil {
				return nil, err
			}
			p.mu.Lock()
			if p.isClosed {
				p.mu.Unlock()
				_ = newClient.Close()
				return nil, fmt.Errorf("DemucsDaemonPool is closed")
			}
			if !p.replaceClientLocked(client, newClient) {
				p.mu.Unlock()
				_ = newClient.Close()
				return nil, fmt.Errorf("DemucsDaemonPool lost client registry entry during replacement")
			}
			p.mu.Unlock()
			return newClient, nil
		}
		return client, nil
	}
}

func (p *DemucsDaemonPool) Release(client *DemucsDaemonClient) {
	if client == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isClosed {
		_ = client.Close()
		return
	}

	// 50 タスクごとに VRAM クリーンアップのためにプロセスをリサイクル
	if client.taskCount >= 50 {
		p.loggerFunc("[DemucsDaemonPool] DemucsDaemon-%d reached task recycling threshold (50 tasks), restarting cleanly...", client.id)
		_ = client.Close()
		newClient, err := startDemucsDaemonProcessComplex(client.id, p.pythonPath, p.workingDir, p.envVars, p.loggerFunc)
		if err == nil {
			if !p.replaceClientLocked(client, newClient) {
				_ = newClient.Close()
				return
			}
			p.idle <- newClient
			return
		}
		p.clients = removeDemucsClient(p.clients, client)
		return
	}

	p.idle <- client
}

func removeDemucsClient(clients []*DemucsDaemonClient, target *DemucsDaemonClient) []*DemucsDaemonClient {
	for i, client := range clients {
		if client == target {
			return append(clients[:i], clients[i+1:]...)
		}
	}
	return clients
}

func (p *DemucsDaemonPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isClosed = true
	close(p.idle)
	for _, c := range p.clients {
		_ = c.Close()
	}
	return nil
}
