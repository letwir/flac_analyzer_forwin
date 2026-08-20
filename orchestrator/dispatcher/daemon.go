// Mor: DaemonRequest -> DaemonResponse
// Functor: f_worker ∘ g_ipc
// Semantics: Category: Persistent Feature Worker Daemon NDJSON IPC Actor
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
	"strings"
	"sync"
	"time"
)

// DaemonRequest represents an NDJSON request sent to worker_daemon.py
type DaemonRequest struct {
	ID      string            `json:"id"`
	Action  string            `json:"action"`
	Payload ExtractAllPayload `json:"payload,omitempty"`
}

// StemInfo represents shared memory or disk file location & dimensions for a single stem
type StemInfo struct {
	ShmTag      string  `json:"shm_tag,omitempty"`
	StorageType string  `json:"storage_type,omitempty"`
	FilePath    string  `json:"file_path,omitempty"`
	Shape       []int64 `json:"shape"`
	Dtype       string  `json:"dtype"`
	FileSize    int64   `json:"file_size,omitempty"`
	SpectroPath string  `json:"spectro_path,omitempty"`
}

// ExtractAllPayload represents the payload sent for extract_all action
type ExtractAllPayload struct {
	SR        int                 `json:"sr"`
	TrackHash string              `json:"track_hash"`
	Stems     map[string]StemInfo `json:"stems"`
}

// DaemonResponse represents an NDJSON response returned by worker_daemon.py
type DaemonResponse struct {
	ID        string                 `json:"id"`
	Status    string                 `json:"status"`
	Error     string                 `json:"error,omitempty"`
	Traceback string                 `json:"traceback,omitempty"`
	Librosa   map[string]interface{} `json:"librosa,omitempty"`
	Tensor    map[string]interface{} `json:"tensor,omitempty"`
	Essentia  map[string]interface{} `json:"essentia,omitempty"`
	Profile   map[string]float64     `json:"profile,omitempty"`
}

// WorkerDaemonClient manages a persistent worker_daemon.py child process over stdin/stdout NDJSON IPC.
type WorkerDaemonClient struct {
	id          int
	pythonPath  string
	parentDir   string
	env         []string
	logger      func(format string, v ...interface{})
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	stderr      io.ReadCloser
	mu          sync.Mutex
	closed      bool
	taskCount   int
	maxRecycle  int
	readyDevice string
}

// NewWorkerDaemonClient starts a new persistent worker_daemon.py process and waits for ready signal.
func NewWorkerDaemonClient(
	id int,
	pythonPath string,
	parentDir string,
	env []string,
	logger func(format string, v ...interface{}),
) (*WorkerDaemonClient, error) {
	client := &WorkerDaemonClient{
		id:         id,
		pythonPath: pythonPath,
		parentDir:  parentDir,
		env:        env,
		logger:     logger,
		maxRecycle: 100,
	}

	if err := client.startProcessComplex(); err != nil {
		return nil, fmt.Errorf("failed to start worker daemon %d: %w", id, err)
	}

	return client, nil
}

func (c *WorkerDaemonClient) startProcessComplex() error {
	scriptPath := filepath.Join(c.parentDir, "worker_daemon.py")
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("worker_daemon.py not found at %s", scriptPath)
	}

	cmd := exec.Command(c.pythonPath, scriptPath)
	cmd.Dir = c.parentDir
	cmd.Env = append(os.Environ(), c.env...)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to open stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return fmt.Errorf("failed to open stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return fmt.Errorf("failed to start cmd: %w", err)
	}

	if cmd.Process != nil {
		if err := AssignPidToJob(cmd.Process.Pid); err != nil && c.logger != nil {
			c.logger("[WorkerDaemon-%d] AssignPidToJob note: %v", c.id, err)
		}
	}

	c.cmd = cmd
	c.stdin = stdinPipe
	c.stdout = bufio.NewReaderSize(stdoutPipe, 1024*1024)
	c.stderr = stderrPipe
	c.closed = false
	c.taskCount = 0

	// Stream stderr in background
	go c.streamStderr(stderrPipe)

	// Handshake: Wait for ready signal (with 30s timeout)
	type readySignal struct {
		Status string `json:"status"`
		Device string `json:"device"`
	}

	readyCh := make(chan error, 1)
	go func() {
		line, readErr := c.stdout.ReadString('\n')
		if readErr != nil {
			readyCh <- fmt.Errorf("failed to read ready signal: %w", readErr)
			return
		}
		var sig readySignal
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(line)), &sig); jsonErr != nil {
			readyCh <- fmt.Errorf("invalid ready signal JSON: %w (raw: %s)", jsonErr, line)
			return
		}
		if sig.Status != "ready" {
			readyCh <- fmt.Errorf("unexpected handshake status: %s", sig.Status)
			return
		}
		c.readyDevice = sig.Device
		readyCh <- nil
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			_ = c.Close()
			return err
		}
	case <-time.After(30 * time.Second):
		_ = c.Close()
		return fmt.Errorf("handshake timeout waiting for worker_daemon.py ready signal")
	}

	if c.logger != nil {
		c.logger("[WorkerDaemon-%d] Ready and attached (Device: %s)", c.id, c.readyDevice)
	}
	return nil
}

func (c *WorkerDaemonClient) streamStderr(pipe io.ReadCloser) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text()
		if c.logger != nil {
			c.logger("%s[Daemon-%d] %s%s", ColorLevel4Cyan, c.id, line, ColorReset)
		}
	}
}

// ExtractAll sends an extract_all request to worker_daemon.py and reads the structured response.
func (c *WorkerDaemonClient) ExtractAll(ctx context.Context, payload ExtractAllPayload) (*DaemonResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.cmd == nil || c.cmd.Process == nil {
		return nil, fmt.Errorf("worker daemon %d is closed or dead", c.id)
	}

	reqID := fmt.Sprintf("req-%d-%d", c.id, time.Now().UnixNano())
	req := DaemonRequest{
		ID:      reqID,
		Action:  "extract_all",
		Payload: payload,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal daemon request: %w", err)
	}

	// Write NDJSON line
	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		_ = c.closeLocked()
		return nil, fmt.Errorf("failed to write request to daemon stdin: %w", err)
	}

	type respResult struct {
		resp *DaemonResponse
		err  error
	}

	resultCh := make(chan respResult, 1)
	go func() {
		line, readErr := c.stdout.ReadString('\n')
		if readErr != nil {
			resultCh <- respResult{err: fmt.Errorf("failed to read response from daemon stdout: %w", readErr)}
			return
		}
		var resp DaemonResponse
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); jsonErr != nil {
			resultCh <- respResult{err: fmt.Errorf("invalid response JSON from daemon: %w (raw: %s)", jsonErr, line)}
			return
		}
		resultCh <- respResult{resp: &resp}
	}()

	select {
	case <-ctx.Done():
		_ = c.closeLocked()
		return nil, fmt.Errorf("daemon ExtractAll context cancelled: %w", ctx.Err())
	case res := <-resultCh:
		if res.err != nil {
			_ = c.closeLocked()
			return nil, res.err
		}
		if res.resp.Status != "success" {
			errMsg := res.resp.Error
			if errMsg == "" {
				errMsg = "daemon returned non-success status"
			}
			return nil, fmt.Errorf("daemon error (%s): %s", res.resp.ID, errMsg)
		}
		c.taskCount++
		return res.resp, nil
	}
}

// Ping checks if the worker daemon is alive and responsive.
func (c *WorkerDaemonClient) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("daemon is closed")
	}

	req := DaemonRequest{
		ID:     fmt.Sprintf("ping-%d", time.Now().UnixNano()),
		Action: "ping",
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal ping request: %w", err)
	}
	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		_ = c.closeLocked()
		return err
	}

	resultCh := make(chan error, 1)
	go func() {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			resultCh <- err
			return
		}
		var resp DaemonResponse
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
			resultCh <- err
			return
		}
		if resp.Status != "pong" {
			resultCh <- fmt.Errorf("unexpected ping response: %s", resp.Status)
			return
		}
		resultCh <- nil
	}()

	select {
	case <-ctx.Done():
		_ = c.closeLocked()
		return ctx.Err()
	case err := <-resultCh:
		if err != nil {
			_ = c.closeLocked()
		}
		return err
	}
}

// IsHealthy returns true if the daemon is running and has not exceeded task recycling limits.
func (c *WorkerDaemonClient) IsHealthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	if c.taskCount >= c.maxRecycle {
		return false
	}
	return true
}

// closeLocked terminates pipes and process while caller already holds c.mu.
func (c *WorkerDaemonClient) closeLocked() error {
	if c.closed {
		return nil
	}
	c.closed = true

	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}

	return nil
}

// Close gracefully closes stdin and terminates the worker daemon process.
func (c *WorkerDaemonClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}
