// Mor: DaemonRequest -> DaemonResponse
// Functor: f_worker ∘ g_ipc
// Semantics: Category: Persistent Feature Worker Daemon NDJSON IPC Actor
package dispatcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

type FeatureLane string

const (
	FeatureLaneCPU FeatureLane = "cpu"
	FeatureLaneGPU FeatureLane = "gpu"
)

type WorkerDaemonRole string

const (
	WorkerDaemonRoleCPU        WorkerDaemonRole = "cpu"
	WorkerDaemonRoleFeatureGPU WorkerDaemonRole = "feature-gpu"
)

func (r WorkerDaemonRole) scriptName() (string, error) {
	switch r {
	case WorkerDaemonRoleCPU:
		return "worker_cpu_daemon.py", nil
	case WorkerDaemonRoleFeatureGPU:
		return "worker_gpu_daemon.py", nil
	default:
		return "", fmt.Errorf("unknown worker daemon role %q", r)
	}
}

func (r WorkerDaemonRole) validReadyDevice(device string) bool {
	switch r {
	case WorkerDaemonRoleCPU:
		return device == "cpu"
	case WorkerDaemonRoleFeatureGPU:
		return device == "cuda"
	default:
		return false
	}
}

// StemInfo represents shared memory or disk file location & dimensions for a single stem
type StemInfo struct {
	ShmTag      string         `json:"shm_tag,omitempty"`
	StorageType string         `json:"storage_type,omitempty"`
	FilePath    string         `json:"file_path,omitempty"`
	Shape       []int64        `json:"shape"`
	Dtype       string         `json:"dtype"`
	FileSize    int64          `json:"file_size,omitempty"`
	SpectroPath string         `json:"spectro_path,omitempty"`
	Stereo      map[string]any `json:"stereo,omitempty"`
	CrossStem   map[string]any `json:"cross_stem,omitempty"`
	SNR         *float64       `json:"snr,omitempty"`
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
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdoutCloser io.Closer
	stdout       *bufio.Reader
	stderr       io.ReadCloser
	rpcSlot      chan struct{}
	mu           sync.Mutex
	closed       bool
	stdinClosed  bool
	stdoutClosed     bool
	stderrClosed     bool
	exited           bool
	waitDone         chan struct{}
	waitErr          error
	waitExited       bool
	pipesDone      chan struct{}
	pipesAllClosed bool
	teardownNotify chan struct{}
	pipeErrs       []error
	activeKill       *daemonKillAttempt
	lateLoggerDone   chan struct{}
	lateLoggerActive int
	taskCount        int
	maxRecycle       int
	readyDevice      string
	role             WorkerDaemonRole
	testHealthy      bool
}

type daemonKillAttempt struct {
	done     chan struct{}
	err      error
	reported bool
}

// NewWorkerDaemonClient starts a new persistent worker_daemon.py process and waits for ready signal.
// Legacy callers always receive (nil, err) on failure; any partially started live child is reaped
// via Close() and, if still exiting after timeout, retained by a background reaper until exit.
func NewWorkerDaemonClient(
	id int,
	pythonPath string,
	parentDir string,
	env []string,
	logger func(format string, v ...interface{}),
	role WorkerDaemonRole,
) (*WorkerDaemonClient, error) {
	client, err := NewWorkerDaemonClientWithGate(id, pythonPath, parentDir, env, logger, role, nil)
	if err != nil {
		if client != nil && (client.HasLiveProcess() || client.HasPendingTeardown()) {
			var closeErr error
			if client.HasLiveProcess() {
				closeErr = client.Close()
			}
			logFn := logger
			if logFn == nil {
				logFn = log.Printf
			}
			client.AttachLateTeardownLogger(logFn)
			if client.HasLiveProcess() {
				go func(c *WorkerDaemonClient) {
					defer func() {
						// Attach a final observer on reaper exit so any error from the last Kill/Close attempt is logged.
						_ = c.AttachLateTeardownLogger(logFn)
					}()
					for c.HasLiveProcess() {
						if sig := c.ExitSignal(); sig != nil {
							select {
							case <-sig:
							case <-time.After(500 * time.Millisecond):
							}
						} else {
							time.Sleep(250 * time.Millisecond)
						}
						if retryErr := c.Close(); retryErr != nil {
							logFn("[WorkerDaemon-%d] background partial-start reap attempt: %v", c.id, retryErr)
						}
					}
				}(client)
			}
			if closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
		}
		return nil, err
	}
	return client, nil
}

// NewWorkerDaemonClientWithGate starts a new persistent worker daemon process, running cmd.Start()
// inside startGate when non-nil so callers can serialize OS child launch against pool shutdown/cancel
// while keeping pipe setup and readiness handshakes outside the pool lock.
func NewWorkerDaemonClientWithGate(
	id int,
	pythonPath string,
	parentDir string,
	env []string,
	logger func(format string, v ...interface{}),
	role WorkerDaemonRole,
	startGate func(startChild func() error) error,
) (*WorkerDaemonClient, error) {
	client := &WorkerDaemonClient{
		id:         id,
		pythonPath: pythonPath,
		parentDir:  parentDir,
		env:        env,
		logger:     logger,
		maxRecycle: 100,
		role:       role,
	}

	if err := client.startProcessComplex(startGate); err != nil {
		if client.HasLiveProcess() || client.HasPendingTeardown() {
			return client, fmt.Errorf("failed to start worker daemon %d: %w", id, err)
		}
		return nil, fmt.Errorf("failed to start worker daemon %d: %w", id, err)
	}

	return client, nil
}

func closeFilesCollectErr(files ...*os.File) []error {
	var errs []error
	for _, f := range files {
		if f == nil {
			continue
		}
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, fmt.Errorf("close pipe handle: %w", err))
		}
	}
	return errs
}

func (c *WorkerDaemonClient) startProcessComplex(startGate func(startChild func() error) error) error {
	scriptName, err := c.role.scriptName()
	if err != nil {
		return err
	}
	scriptPath := filepath.Join(c.parentDir, scriptName)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("%s not found at %s", scriptName, scriptPath)
	}

	cmd := exec.Command(c.pythonPath, scriptPath)
	cmd.Dir = c.parentDir
	cmd.Env = append(os.Environ(), c.env...)

	startChild := func() error {
		stdinR, stdinW, pipeErr := os.Pipe()
		if pipeErr != nil {
			return fmt.Errorf("failed to open stdin pipe: %w", pipeErr)
		}
		stdoutR, stdoutW, pipeErr := os.Pipe()
		if pipeErr != nil {
			errs := append([]error{fmt.Errorf("failed to open stdout pipe: %w", pipeErr)}, closeFilesCollectErr(stdinR, stdinW)...)
			return errors.Join(errs...)
		}
		stderrR, stderrW, pipeErr := os.Pipe()
		if pipeErr != nil {
			errs := append([]error{fmt.Errorf("failed to open stderr pipe: %w", pipeErr)}, closeFilesCollectErr(stdinR, stdinW, stdoutR, stdoutW)...)
			return errors.Join(errs...)
		}
		cmd.Stdin = stdinR
		cmd.Stdout = stdoutW
		cmd.Stderr = stderrW
		startErr := cmd.Start()
		if startErr != nil {
			errs := append([]error{fmt.Errorf("failed to start cmd: %w", startErr)}, closeFilesCollectErr(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)...)
			return errors.Join(errs...)
		}
		// Record child and parent pipe ownership immediately upon OS start so any
		// post-launch close failure or gate error cleans up pipes and kills/waits for the child.
		c.mu.Lock()
		c.cmd = cmd
		c.stdin = stdinW
		c.stdoutCloser = stdoutR
		c.stdout = bufio.NewReaderSize(stdoutR, 1024*1024)
		c.stderr = stderrR
		c.closed = false
		c.stdinClosed = false
		c.stdoutClosed = false
		c.stderrClosed = false
		c.taskCount = 0
		c.mu.Unlock()
		// Child ends are no longer needed by parent after cmd.Start() succeeds;
		// propagate any close failure into startup rollback rather than silently logging.
		if childCloseErrs := closeFilesCollectErr(stdinR, stdoutW, stderrW); len(childCloseErrs) > 0 {
			return errors.Join(childCloseErrs...)
		}
		return nil
	}

	var startErr error
	if startGate != nil {
		startErr = startGate(startChild)
	} else {
		startErr = startChild()
	}
	if cmd.Process != nil {
		if assignErr := AssignPidToJob(cmd.Process.Pid); assignErr != nil && c.logger != nil {
			c.logger("[WorkerDaemon-%d] AssignPidToJob note: %v", c.id, assignErr)
		}
	}
	if startErr != nil {
		if c.HasLiveProcess() {
			if closeErr := c.Close(); closeErr != nil {
				return errors.Join(fmt.Errorf("failed to start cmd: %w", startErr), fmt.Errorf("rollback daemon %d close: %w", c.id, closeErr))
			}
		}
		return fmt.Errorf("failed to start cmd: %w", startErr)
	}

	c.mu.Lock()
	initialized := c.cmd != nil && c.stdout != nil && c.stderr != nil && !c.closed
	stderrPipe := c.stderr
	c.mu.Unlock()
	if !initialized {
		if c.HasLiveProcess() {
			if closeErr := c.Close(); closeErr != nil {
				return errors.Join(fmt.Errorf("failed to start cmd: daemon pipes/process not initialized by start gate"), fmt.Errorf("rollback daemon %d close: %w", c.id, closeErr))
			}
		}
		return fmt.Errorf("failed to start cmd: daemon pipes/process not initialized by start gate")
	}

	// Stream stderr in background
	go c.streamStderr(stderrPipe)

	// Handshake: Wait for ready signal (with 30s timeout)
	type readySignal struct {
		Status string `json:"status"`
		Role   string `json:"role"`
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
		if sig.Role != string(c.role) || !c.role.validReadyDevice(sig.Device) {
			readyCh <- fmt.Errorf("worker daemon role/device mismatch: want %s/%s, got %s/%s", c.role, expectedDeviceForRole(c.role), sig.Role, sig.Device)
			return
		}
		c.readyDevice = sig.Device
		readyCh <- nil
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			if closeErr := c.Close(); closeErr != nil {
				return errors.Join(err, fmt.Errorf("rollback close failed: %w", closeErr))
			}
			return err
		}
	case <-time.After(30 * time.Second):
		timeoutErr := fmt.Errorf("handshake timeout waiting for %s ready signal", scriptName)
		if closeErr := c.Close(); closeErr != nil {
			return errors.Join(timeoutErr, fmt.Errorf("rollback close failed: %w", closeErr))
		}
		return timeoutErr
	}

	if c.logger != nil {
		c.logger("[WorkerDaemon-%d] Ready and attached (Device: %s)", c.id, c.readyDevice)
	}
	return nil
}

func expectedDeviceForRole(role WorkerDaemonRole) string {
	if role == WorkerDaemonRoleCPU {
		return "cpu"
	}
	return "cuda"
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

// ExtractAll sends the legacy extract_all request to worker_daemon.py and reads the structured response.
func (c *WorkerDaemonClient) ExtractAll(ctx context.Context, payload ExtractAllPayload) (*DaemonResponse, error) {
	return c.extract(ctx, "extract_all", payload)
}

// ExtractCPU sends the bounded Librosa and Essentia lane to a worker daemon.
func (c *WorkerDaemonClient) ExtractCPU(ctx context.Context, payload ExtractAllPayload) (*DaemonResponse, error) {
	return c.extract(ctx, "extract_cpu", payload)
}

// ExtractGPU sends the bounded Tensor lane to a worker daemon.
func (c *WorkerDaemonClient) ExtractGPU(ctx context.Context, payload ExtractAllPayload) (*DaemonResponse, error) {
	return c.extract(ctx, "extract_gpu", payload)
}

func (c *WorkerDaemonClient) ensureRpcSlotLocked() chan struct{} {
	if c.rpcSlot == nil {
		c.rpcSlot = make(chan struct{}, 1)
		c.rpcSlot <- struct{}{}
	}
	return c.rpcSlot
}

// roundTripJSON acquires the per-client RPC slot (cancellable via ctx), sends one NDJSON request,
// reads and validates the reply, and runs any failure teardown BEFORE releasing the RPC slot.
// Blocking pipe I/O never holds c.mu, so cancellation and Close() never stall on c.mu.
func (c *WorkerDaemonClient) roundTripJSON(
	ctx context.Context,
	req DaemonRequest,
	opName string,
	expectedStatus string,
	closeOnBadStatus bool,
	countTask bool,
) (*DaemonResponse, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s request: %w", opName, err)
	}

	c.mu.Lock()
	slot := c.ensureRpcSlotLocked()
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("daemon %s context cancelled waiting for RPC slot: %w", opName, ctx.Err())
	case <-slot:
	}
	defer func() {
		slot <- struct{}{}
	}()

	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("daemon %s request cancelled before write: %w", opName, err)
	}
	live := c.testHealthy || (c.cmd != nil && c.cmd.Process != nil)
	if c.closed || !live || c.stdin == nil || c.stdout == nil {
		id := c.id
		c.mu.Unlock()
		return nil, fmt.Errorf("worker daemon %d is closed or dead", id)
	}
	stdin := c.stdin
	stdout := c.stdout
	c.mu.Unlock()

	lineBytes := append(reqBytes, '\n')
	writeDone := make(chan error, 1)
	go func() {
		_, wErr := stdin.Write(lineBytes)
		writeDone <- wErr
	}()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		err := c.closeAndJoinWithContextLocked(ctx, fmt.Errorf("daemon %s context cancelled: %w", opName, ctx.Err()))
		c.mu.Unlock()
		return nil, err
	case wErr := <-writeDone:
		if wErr != nil {
			c.mu.Lock()
			err := c.closeAndJoinWithContextLocked(ctx, fmt.Errorf("failed to write %s request to daemon stdin: %w", opName, wErr))
			c.mu.Unlock()
			return nil, err
		}
	}

	type readResult struct {
		resp *DaemonResponse
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		line, readErr := stdout.ReadString('\n')
		if readErr != nil {
			readCh <- readResult{err: fmt.Errorf("failed to read %s response from daemon stdout: %w", opName, readErr)}
			return
		}
		var resp DaemonResponse
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); jsonErr != nil {
			readCh <- readResult{err: fmt.Errorf("invalid response JSON from daemon for %s: %w (raw: %s)", opName, jsonErr, line)}
			return
		}
		readCh <- readResult{resp: &resp}
	}()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		err := c.closeAndJoinWithContextLocked(ctx, fmt.Errorf("daemon %s context cancelled: %w", opName, ctx.Err()))
		c.mu.Unlock()
		return nil, err
	case res := <-readCh:
		if res.err != nil {
			c.mu.Lock()
			err := c.closeAndJoinWithContextLocked(ctx, res.err)
			c.mu.Unlock()
			return nil, err
		}
		if res.resp.Status != expectedStatus {
			errMsg := res.resp.Error
			var protoErr error
			if errMsg != "" {
				protoErr = fmt.Errorf("daemon error (%s): %s", res.resp.ID, errMsg)
			} else if opName == "ping" {
				protoErr = fmt.Errorf("unexpected ping response: %s", res.resp.Status)
			} else {
				protoErr = fmt.Errorf("daemon returned non-success status for %s: %s", opName, res.resp.Status)
			}
			if closeOnBadStatus {
				c.mu.Lock()
				closeErr := c.closeAndJoinWithContextLocked(ctx, protoErr)
				c.mu.Unlock()
				return nil, closeErr
			}
			return nil, protoErr
		}
		if countTask {
			c.mu.Lock()
			c.taskCount++
			c.mu.Unlock()
		}
		return res.resp, nil
	}
}

// CleanupGPU sends the cleanup_gpu request to worker_daemon.py
func (c *WorkerDaemonClient) CleanupGPU(ctx context.Context) error {
	req := DaemonRequest{
		ID:     fmt.Sprintf("req-%d-%d", c.id, time.Now().UnixNano()),
		Action: "cleanup_gpu",
	}
	_, err := c.roundTripJSON(ctx, req, "cleanup_gpu", "success", true, true)
	return err
}

func (c *WorkerDaemonClient) extract(ctx context.Context, action string, payload ExtractAllPayload) (*DaemonResponse, error) {
	req := DaemonRequest{
		ID:      fmt.Sprintf("req-%d-%d", c.id, time.Now().UnixNano()),
		Action:  action,
		Payload: payload,
	}
	return c.roundTripJSON(ctx, req, action, "success", false, true)
}

// Ping checks if the worker daemon is alive and responsive.
func (c *WorkerDaemonClient) Ping(ctx context.Context) error {
	req := DaemonRequest{
		ID:     fmt.Sprintf("ping-%d", time.Now().UnixNano()),
		Action: "ping",
	}
	_, err := c.roundTripJSON(ctx, req, "ping", "pong", true, false)
	return err
}

// IsHealthy returns true if the daemon is running and has not exceeded task recycling limits.
func (c *WorkerDaemonClient) IsHealthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.testHealthy {
		return !c.closed
	}
	if c.closed || c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	if c.taskCount >= c.maxRecycle {
		return false
	}
	return true
}

// HasLiveProcess reports whether this client may still have an unconfirmed OS process.
func (c *WorkerDaemonClient) HasLiveProcess() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exited {
		return false
	}
	return (c.cmd != nil && c.cmd.Process != nil) || c.waitDone != nil
}

// ExitSignal returns a channel closed when the active OS cmd.Wait() finishes, or nil if no active
// waiter is still pending (so watchers fall back to timed backoff instead of busy-spinning).
func (c *WorkerDaemonClient) ExitSignal() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exited || c.waitDone == nil {
		return nil
	}
	select {
	case <-c.waitDone:
		return nil
	default:
		return c.waitDone
	}
}

// HasPendingTeardown reports whether parent pipe closure, a kill attempt, or any unclaimed
// teardown errors are still active on this client.
func (c *WorkerDaemonClient) HasPendingTeardown() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if (c.pipesDone != nil && !c.pipesAllClosed) || len(c.pipeErrs) > 0 {
		return true
	}
	if c.activeKill != nil {
		select {
		case <-c.activeKill.done:
			if c.activeKill.err != nil && !c.activeKill.reported {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// SubscribeTeardownErrors atomically claims any currently queued parent pipe or kill errors,
// reports whether all started pipe closes and kill attempts have finished, and returns a
// broadcast channel signaled whenever a new teardown error arrives or teardown finishes.
func (c *WorkerDaemonClient) SubscribeTeardownErrors() (errs []error, allDone bool, notify <-chan struct{}) {
	if c == nil {
		return nil, true, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	errs = append(errs, c.claimActiveKillErrLocked()...)
	errs = append(errs, c.claimPipeErrsLocked()...)

	pipesPending := c.pipesDone != nil && !c.pipesAllClosed
	killPending := false
	if c.activeKill != nil {
		select {
		case <-c.activeKill.done:
		default:
			killPending = true
		}
	}
	if !pipesPending && !killPending {
		return errs, true, nil
	}
	if c.teardownNotify == nil {
		c.teardownNotify = make(chan struct{})
	}
	return errs, false, c.teardownNotify
}

// AttachLateTeardownLogger drains all pending and subsequent pipe/kill teardown errors via
// logFn in a background observer, returning immediately with a channel closed only after
// every attached observer callback finishes.
func (c *WorkerDaemonClient) AttachLateTeardownLogger(logFn func(string, ...interface{})) <-chan struct{} {
	if c == nil || logFn == nil {
		doneCh := make(chan struct{})
		close(doneCh)
		return doneCh
	}
	c.mu.Lock()
	if c.lateLoggerActive == 0 {
		c.lateLoggerDone = make(chan struct{})
	}
	doneCh := c.lateLoggerDone
	c.lateLoggerActive++
	c.mu.Unlock()

	releaseObserver := func() {
		c.mu.Lock()
		c.lateLoggerActive--
		if c.lateLoggerActive == 0 {
			close(doneCh)
		}
		c.mu.Unlock()
	}

	errs, done, notify := c.SubscribeTeardownErrors()
	if len(errs) == 0 && (done || notify == nil) {
		releaseObserver()
		return doneCh
	}
	go func(initialErrs []error, initialDone bool, ch <-chan struct{}) {
		defer releaseObserver()
		if len(initialErrs) > 0 {
			logFn("[DaemonPool] Retired daemon-%d teardown error: %v", c.id, errors.Join(initialErrs...))
		}
		if initialDone || ch == nil {
			return
		}
		for {
			<-ch
			nextErrs, nextDone, nextCh := c.SubscribeTeardownErrors()
			if len(nextErrs) > 0 {
				logFn("[DaemonPool] Retired daemon-%d late teardown error: %v", c.id, errors.Join(nextErrs...))
			}
			if nextDone || nextCh == nil {
				return
			}
			ch = nextCh
		}
	}(errs, done, notify)
	return doneCh
}

// LateTeardownDone returns the completion channel of the most recently attached teardown logger, if any.
func (c *WorkerDaemonClient) LateTeardownDone() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lateLoggerDone
}

// PipesSignal returns a channel closed once all parallel parent pipe closes finish, or nil if already done.
func (c *WorkerDaemonClient) PipesSignal() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pipesDone == nil || c.pipesAllClosed {
		return nil
	}
	return c.pipesDone
}

// DrainPipeErrors claims and returns any unreported parent pipe close errors collected so far.
func (c *WorkerDaemonClient) DrainPipeErrors() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	errs := c.claimPipeErrsLocked()
	c.mu.Unlock()
	return errors.Join(errs...)
}

// AwaitExit kills and waits until the underlying daemon process exit is confirmed or ctx is canceled.
func (c *WorkerDaemonClient) AwaitExit(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeWithContextLocked(ctx, true)
}

// CloseForRetirement initiates parallel pipe closure and kills/waits for OS exit within ctx
// without blocking pool retirement on a stalled Windows parent pipe close once the child is reaped.
func (c *WorkerDaemonClient) CloseForRetirement(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeWithContextLocked(ctx, false)
}

// closeLocked terminates pipes and process while caller holds c.mu (up to 2s bounded wait).
func (c *WorkerDaemonClient) closeLocked() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.closeWithContextLocked(ctx, true)
}

func (c *WorkerDaemonClient) closeAndJoinWithContextLocked(ctx context.Context, baseErr error) error {
	if closeErr := c.closeWithContextLocked(ctx, true); closeErr != nil {
		return errors.Join(baseErr, fmt.Errorf("teardown daemon %d: %w", c.id, closeErr))
	}
	return baseErr
}

type pipeCloserEntry struct {
	name   string
	closer io.Closer
}

func (c *WorkerDaemonClient) takePipeClosersLocked() []pipeCloserEntry {
	var list []pipeCloserEntry
	for _, p := range []struct {
		name   string
		closer io.Closer
		closed *bool
	}{
		{"stdin", c.stdin, &c.stdinClosed},
		{"stdout", c.stdoutCloser, &c.stdoutClosed},
		{"stderr", c.stderr, &c.stderrClosed},
	} {
		if p.closer != nil && !*p.closed {
			*p.closed = true
			list = append(list, pipeCloserEntry{name: p.name, closer: p.closer})
		}
	}
	return list
}

func (c *WorkerDaemonClient) claimPipeErrsLocked() []error {
	if len(c.pipeErrs) == 0 {
		return nil
	}
	errs := c.pipeErrs
	c.pipeErrs = nil
	return errs
}

func (c *WorkerDaemonClient) broadcastTeardownNotifyLocked() {
	if c.teardownNotify != nil {
		close(c.teardownNotify)
		c.teardownNotify = make(chan struct{})
	}
}

func (c *WorkerDaemonClient) ensurePipesTeardownLocked() <-chan struct{} {
	if c.pipesDone != nil {
		return c.pipesDone
	}
	done := make(chan struct{})
	c.pipesDone = done
	pipes := c.takePipeClosersLocked()
	if len(pipes) == 0 {
		c.pipesAllClosed = true
		close(done)
		c.broadcastTeardownNotifyLocked()
		return done
	}
	go func() {
		var pwg sync.WaitGroup
		for _, entry := range pipes {
			pwg.Add(1)
			go func(p pipeCloserEntry) {
				defer pwg.Done()
				if err := p.closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					pErr := fmt.Errorf("close daemon %s: %w", p.name, err)
					c.mu.Lock()
					c.pipeErrs = append(c.pipeErrs, pErr)
					c.broadcastTeardownNotifyLocked()
					c.mu.Unlock()
				}
			}(entry)
		}
		pwg.Wait()
		c.mu.Lock()
		c.pipesAllClosed = true
		close(done)
		c.broadcastTeardownNotifyLocked()
		c.mu.Unlock()
	}()
	return done
}

func (c *WorkerDaemonClient) claimActiveKillErrLocked() []error {
	if c.activeKill == nil {
		return nil
	}
	select {
	case <-c.activeKill.done:
		if c.activeKill.err != nil && !c.activeKill.reported {
			c.activeKill.reported = true
			return []error{c.activeKill.err}
		}
	default:
	}
	return nil
}

func (c *WorkerDaemonClient) acquireKillAttemptLocked(proc *os.Process) (*daemonKillAttempt, error) {
	var priorErr error
	if c.activeKill != nil {
		select {
		case <-c.activeKill.done:
			// Reuse completed kill only when it succeeded; allow retry only after an error.
			if c.activeKill.err == nil {
				return c.activeKill, nil
			}
			if !c.activeKill.reported {
				c.activeKill.reported = true
				priorErr = c.activeKill.err
			}
			c.activeKill = nil
		default:
			// Share single in-flight kill attempt rather than queueing duplicate goroutines on stalled kills.
			return c.activeKill, nil
		}
	}
	ka := &daemonKillAttempt{done: make(chan struct{})}
	c.activeKill = ka
	id := c.id
	go func(p *os.Process, attempt *daemonKillAttempt) {
		var killErr error
		if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			killErr = fmt.Errorf("kill worker daemon %d: %w", id, err)
		}
		c.mu.Lock()
		attempt.err = killErr
		close(attempt.done)
		c.broadcastTeardownNotifyLocked()
		c.mu.Unlock()
	}(proc, ka)
	return ka, priorErr
}

func (c *WorkerDaemonClient) collectCompletedTeardownErrsLocked(ka *daemonKillAttempt) []error {
	var errs []error
	if ka != nil {
		select {
		case <-ka.done:
			if ka.err != nil && !ka.reported {
				ka.reported = true
				errs = append(errs, ka.err)
			}
		default:
		}
	} else {
		errs = append(errs, c.claimActiveKillErrLocked()...)
	}
	errs = append(errs, c.claimPipeErrsLocked()...)
	return errs
}

func (c *WorkerDaemonClient) ensureWaitLocked() <-chan struct{} {
	if c.waitDone != nil {
		return c.waitDone
	}
	done := make(chan struct{})
	c.waitDone = done
	cmd := c.cmd
	if c.exited || cmd == nil || cmd.Process == nil {
		c.waitExited = true
		close(done)
		return done
	}
	go func() {
		waitErr := cmd.Wait()
		exited := cmd.ProcessState != nil
		c.mu.Lock()
		c.waitErr = waitErr
		c.waitExited = exited
		if exited {
			c.exited = true
		}
		c.mu.Unlock()
		close(done)
	}()
	return done
}

// closeWithContextLocked decouples singleflight retriable-on-error Kill(), parallel parent pipe
// closure, and singleflight cmd.Wait() so callers wait bounded by ctx without holding c.mu across
// syscalls or growing retry goroutines on stalled kills. Unclaimed kill and pipe errors remain
// claimable by any subsequent Close() (even after exit confirmation) or SubscribePipeErrors().
// Note: returns with c.mu held.
func (c *WorkerDaemonClient) closeWithContextLocked(ctx context.Context, awaitPipes bool) error {
	c.closed = true
	id := c.id
	pipesDone := c.ensurePipesTeardownLocked()

	var proc *os.Process
	if c.cmd != nil {
		proc = c.cmd.Process
	}
	needsWait := !c.exited && proc != nil
	var waitDone <-chan struct{}
	killAttempt := c.activeKill
	var priorKillErr error
	if needsWait {
		waitDone = c.ensureWaitLocked()
		killAttempt, priorKillErr = c.acquireKillAttemptLocked(proc)
	} else {
		c.exited = true
	}

	collectLocked := func() []error {
		var errs []error
		if priorKillErr != nil {
			errs = append(errs, priorKillErr)
			priorKillErr = nil
		}
		return append(errs, c.collectCompletedTeardownErrsLocked(killAttempt)...)
	}

	c.mu.Unlock()

	if needsWait && killAttempt != nil {
		select {
		case <-killAttempt.done:
		case <-ctx.Done():
			c.mu.Lock()
			errs := append(collectLocked(), fmt.Errorf("waiting for worker daemon %d kill: %w", id, ctx.Err()))
			return errors.Join(errs...)
		}
	}

	if awaitPipes {
		select {
		case <-pipesDone:
		case <-ctx.Done():
			c.mu.Lock()
			errs := append(collectLocked(), fmt.Errorf("waiting for worker daemon %d pipe close: %w", id, ctx.Err()))
			return errors.Join(errs...)
		}
	}

	if waitDone != nil {
		select {
		case <-waitDone:
		case <-ctx.Done():
			c.mu.Lock()
			errs := append(collectLocked(), fmt.Errorf("waiting for worker daemon %d exit: %w", id, ctx.Err()))
			return errors.Join(errs...)
		}
	}

	c.mu.Lock()
	closeErrs := collectLocked()
	if needsWait {
		if c.waitExited {
			c.exited = true
		} else {
			if c.waitErr != nil {
				closeErrs = append(closeErrs, fmt.Errorf("worker daemon %d wait failed without exit confirmation: %w", id, c.waitErr))
			} else {
				closeErrs = append(closeErrs, fmt.Errorf("worker daemon %d wait returned without exited ProcessState", id))
			}
		}
	}
	return errors.Join(closeErrs...)
}

// Close gracefully closes stdin and terminates the worker daemon process.
func (c *WorkerDaemonClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}
