package wasmskill

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"
)

// Session errors.
var (
	// ErrSessionClosed is returned for operations after Close.
	ErrSessionClosed = errors.New("wasmskill: session closed")
	// ErrBadGuestResponse is returned when a guest line is not a valid
	// NDJSON/JSON-RPC 2.0 response.
	ErrBadGuestResponse = errors.New("wasmskill: malformed guest response")
)

// SkillError is a classified failure of a skill session. Class and
// Retryable tell the caller how to react: sandbox and logic errors are
// stable facts (repeating the same call fails the same way), internal
// errors may clear on retry.
type SkillError struct {
	// Class is "sandbox", "logic", or "internal".
	Class string
	// Retryable reports whether repeating the call may succeed.
	Retryable bool
	// Err is the underlying error.
	Err error
}

// Error implements error.
func (e *SkillError) Error() string {
	if e.Err == nil {
		return "wasmskill: " + e.Class
	}
	return fmt.Sprintf("wasmskill: %s: %v", e.Class, e.Err)
}

// Unwrap implements errors.Unwrap.
func (e *SkillError) Unwrap() error { return e.Err }

// newSkillError builds a classified error.
func newSkillError(class string, retryable bool, err error) *SkillError {
	return &SkillError{Class: class, Retryable: retryable, Err: err}
}

// classifyGuest maps a guest-reported error to host policy: a guest may
// claim "sandbox" or "logic" (both non-retryable), but only "internal" is
// retryable, and an unknown class is treated as internal. A nil data object
// is an internal, retryable error.
func classifyGuest(g *GuestError) *SkillError {
	if g == nil {
		return newSkillError(ErrClassInternal, true, errors.New("guest returned an RPC error without detail"))
	}
	msg := g.Message
	if msg == "" {
		msg = "guest returned an RPC error"
	}
	switch g.Class {
	case ErrClassSandbox:
		return newSkillError(ErrClassSandbox, false, errors.New(msg))
	case ErrClassLogic:
		return newSkillError(ErrClassLogic, false, errors.New(msg))
	default:
		return newSkillError(ErrClassInternal, true, errors.New(msg))
	}
}

// Skill is one live session with a Wasm skill server: a bounded init
// handshake, many bounded invokes, and a bounded close (best-effort
// cleanup probe, then EOF, then kill).
//
// A Skill is safe for concurrent use; the contexts passed to its methods
// bound each call.
type Skill struct {
	mu       sync.Mutex
	readMu   sync.Mutex // bufio.Reader is not safe for concurrent reads.
	cfg      Config
	host     *host
	mod      api.Module
	in       io.WriteCloser
	out      *bufio.Reader
	stderr   *bytes.Buffer
	kill     context.CancelFunc
	done     chan struct{} // closed when the guest's _start returns
	startErr error
	seq      int64
	tools    []ToolDef
	closed   bool
}

// NewSkill compiles wasm, admits it against the skill boundary, starts the
// guest, and performs the init handshake. On any failure the guest is
// killed and the runtime closed.
func NewSkill(ctx context.Context, wasm []byte, cfg *Config, init InitRequest) (*Skill, error) {
	c, err := cfg.effective()
	if err != nil {
		return nil, err
	}
	if init.Meta.Version == 0 {
		init.Meta.Version = 1
	}
	h, err := newHost(ctx)
	if err != nil {
		return nil, err
	}
	compiled, err := h.rt.CompileModule(ctx, wasm)
	if err != nil {
		h.rt.Close(context.Background())
		return nil, fmt.Errorf("%w: %v", ErrInvalidModule, err)
	}
	if err := admit(compiled); err != nil {
		h.rt.Close(context.Background())
		return nil, err
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &bytes.Buffer{}
	mod, err := h.rt.InstantiateModule(ctx, compiled, moduleConfig(c, inR, outW, stderr))
	if err != nil {
		h.rt.Close(context.Background())
		return nil, fmt.Errorf("wasmskill: instantiating module %q: %w", c.Name, err)
	}

	gctx, kill := context.WithCancel(ctx)
	s := &Skill{
		cfg:    c,
		host:   h,
		mod:    mod,
		in:     inW,
		out:    bufio.NewReader(outR),
		stderr: stderr,
		kill:   kill,
		done:   make(chan struct{}),
	}
	go func() {
		_, callErr := mod.ExportedFunction("_start").Call(gctx)
		s.mu.Lock()
		if s.startErr == nil {
			s.startErr = callErr
		}
		s.mu.Unlock()
		_ = outW.Close()
		close(s.done)
	}()

	initCtx, cancel := context.WithTimeout(ctx, c.InitTimeout)
	defer cancel()
	if err := s.writeLine(rpcRequest{JSONRPC: "2.0", ID: 1, Method: MethodInit, Params: init}); err != nil {
		s.teardown()
		return nil, err
	}
	line, err := s.readLine(initCtx)
	if err != nil {
		s.teardown()
		return nil, err
	}
	resp, err := parseResponse(line)
	if err != nil {
		s.teardown()
		return nil, newSkillError(ErrClassInternal, true, fmt.Errorf("%w: init: %w", ErrBadGuestResponse, err))
	}
	if resp.Error != nil {
		s.teardown()
		return nil, classifyGuest(resp.Error.Data)
	}
	var res InitResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		s.teardown()
		return nil, newSkillError(ErrClassInternal, true, fmt.Errorf("wasmskill: decoding init result: %w", err))
	}
	s.tools = res.Tools
	return s, nil
}

// Tools returns the tools the guest advertised in its init response.
func (s *Skill) Tools() []ToolDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ToolDef, len(s.tools))
	copy(out, s.tools)
	return out
}

// Stderr returns the guest's stderr captured so far (diagnostics only;
// stderr is never parsed).
func (s *Skill) Stderr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stderr.String()
}

// Invoke calls one tool of the skill and waits for its result, bounded by
// ctx (or the configured InvokeTimeout when ctx has no deadline). A guest
// that reports an RPC error yields a *SkillError (class sandbox/logic/
// internal); a guest that dies mid-call yields a retryable internal
// *SkillError.
func (s *Skill) Invoke(ctx context.Context, req InvokeRequest) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Result{}, newSkillError(ErrClassInternal, false, ErrSessionClosed)
	}
	if s.dead() {
		return Result{}, s.sessionErr()
	}

	inCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		inCtx, cancel = context.WithTimeout(ctx, s.cfg.InvokeTimeout)
		defer cancel()
	}
	watchCtx, watchCancel := context.WithCancel(inCtx)
	defer watchCancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-watchCtx.Done():
			s.kill()
		case <-stop:
		case <-s.done:
		}
	}()

	s.seq++
	if err := s.writeLine(rpcRequest{JSONRPC: "2.0", ID: s.seq, Method: MethodInvoke, Params: req}); err != nil {
		return Result{}, newSkillError(ErrClassInternal, true, fmt.Errorf("wasmskill: writing to guest: %w", err))
	}
	// The guest-exit goroutine needs s.mu to publish startErr and close stdout.
	// Do not hold the session lock while waiting for that stdout read.
	s.mu.Unlock()
	line, err := s.readLine(inCtx)
	s.mu.Lock()
	if s.closed {
		return Result{}, newSkillError(ErrClassInternal, false, ErrSessionClosed)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return Result{}, newSkillError(ErrClassInternal, true, fmt.Errorf("wasmskill: invoke deadline: %w", err))
		}
		return Result{}, s.sessionErr()
	}
	resp, err := parseResponse(line)
	if err != nil {
		return Result{}, newSkillError(ErrClassInternal, true, fmt.Errorf("%w: %w", ErrBadGuestResponse, err))
	}
	if resp.Error != nil {
		return Result{}, classifyGuest(resp.Error.Data)
	}
	var res Result
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return Result{}, newSkillError(ErrClassInternal, true, fmt.Errorf("wasmskill: decoding invoke result: %w", err))
	}
	return res, nil
}

// Close ends the session: it probes the guest with a best-effort
// "skill_cleanup" request (bounded at 2 s), then sends EOF on stdin, then
// kills the module if it has not exited within 5 s, and closes the runtime.
// Close is always finite and idempotent.
func (s *Skill) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	// Session shutdown waits for the guest to finish. Its goroutine needs
	// s.mu to publish the exit error, so do not hold the lock while waiting.
	s.mu.Unlock()

	if !s.dead() {
		// Best-effort cleanup probe.
		s.seq++
		req := rpcRequest{JSONRPC: "2.0", ID: s.seq, Method: MethodCleanup, Params: struct{}{}}
		wrote := make(chan struct{})
		go func() {
			_ = s.writeLine(req)
			close(wrote)
		}()
		select {
		case <-wrote:
		case <-time.After(2 * time.Second):
		case <-s.done:
		}
		got := make(chan struct{})
		go func() {
			s.readMu.Lock()
			_, _ = s.out.ReadString('\n')
			s.readMu.Unlock()
			close(got)
		}()
		select {
		case <-got:
		case <-time.After(2 * time.Second):
		case <-s.done:
		}
	}
	_ = s.in.Close()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		s.kill()
		<-s.done
	case <-ctx.Done():
		s.kill()
		<-s.done
	}
	_ = s.host.rt.Close(context.Background())
	return nil
}

// readLine reads one NDJSON line from the guest, bounded by ctx: when the
// context expires before a full line arrives, the guest is killed (its
// stdout closes and the read returns). It does not require s.mu.
func (s *Skill) readLine(ctx context.Context) ([]byte, error) {
	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		s.readMu.Lock()
		line, err := s.out.ReadString('\n')
		s.readMu.Unlock()
		ch <- readResult{line: []byte(line), err: err}
	}()
	select {
	case result := <-ch:
		if result.err != nil && !bytes.HasSuffix(result.line, []byte("\n")) {
			return result.line, result.err
		}
		return result.line, nil
	case <-ctx.Done():
		s.kill()
		select {
		case <-ch:
		case <-s.done:
		}
		return nil, ctx.Err()
	}
}

// writeLine writes one NDJSON request line to the guest stdin. Callers
// serialize session operations with s.mu; Close's cleanup write is safe
// without s.mu because it marks the session closed before writing.
func (s *Skill) writeLine(req rpcRequest) error {
	b, err := encodeLine(req)
	if err != nil {
		return err
	}
	if _, err := s.in.Write(b); err != nil {
		return fmt.Errorf("wasmskill: writing to guest: %w", err)
	}
	return nil
}

// dead reports whether the guest's _start has returned (always true after
// Close). It only observes s.done and does not require s.mu.
func (s *Skill) dead() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// sessionErr classifies the death of the session. Callers must hold s.mu
// while the guest may still be running; after s.done is closed, its close
// synchronizes access to startErr.
func (s *Skill) sessionErr() *SkillError {
	if s.startErr == nil {
		return newSkillError(ErrClassInternal, true, errors.New("wasmskill: guest exited the session"))
	}
	var ex *sys.ExitError
	if errors.As(s.startErr, &ex) {
		return newSkillError(ErrClassInternal, true, fmt.Errorf("wasmskill: guest exited with code %d", ex.ExitCode()))
	}
	return newSkillError(ErrClassInternal, true, s.startErr)
}

// teardown kills the guest and closes the runtime. Do not hold s.mu when
// calling teardown: it waits for the guest goroutine, which needs s.mu to
// publish its exit error.
func (s *Skill) teardown() {
	s.kill()
	<-s.done
	_ = s.in.Close()
	_ = s.host.rt.Close(context.Background())
}
