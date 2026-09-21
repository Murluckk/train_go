package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var errCanceledByUser = errors.New("canceled by user")

// retainFinished is how long a finished run stays addressable so that a slow
// or reconnecting browser can still read its output.
const retainFinished = 30 * time.Minute

// Config configures a Runner.
type Config struct {
	// GoBin is the go command. Empty means look it up in PATH.
	GoBin string
	// CacheDir is GOCACHE. It must be stable across runs: putting the build
	// cache in the per-run temporary directory turns every run into a full
	// rebuild of the standard library, with -race, from scratch.
	CacheDir string
	// WorkDir is the parent of the sandboxes. Empty means the system temp.
	WorkDir string
	// RunTimeout is passed to go test -timeout.
	RunTimeout time.Duration
	// HardTimeout bounds the whole command. It must exceed RunTimeout so
	// that, normally, go test times out first and prints its goroutine dump,
	// which is exactly the diagnosis a stuck-goroutine exercise needs.
	HardTimeout time.Duration
	// MaxParallel bounds concurrent runs.
	MaxParallel int
	Logger      *slog.Logger
}

// Runner owns the sandboxes and the running processes.
type Runner struct {
	cfg       Config
	goBin     string
	goVersion string
	log       *slog.Logger
	sem       chan struct{}

	mu   sync.Mutex
	runs map[string]*Run
	wg   sync.WaitGroup
}

// New prepares a Runner, resolving the go toolchain and creating the build
// cache directory.
func New(cfg Config) (*Runner, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxParallel < 1 {
		cfg.MaxParallel = 1
	}
	if cfg.HardTimeout <= cfg.RunTimeout {
		return nil, fmt.Errorf("hard timeout %s must exceed run timeout %s", cfg.HardTimeout, cfg.RunTimeout)
	}

	goBin := cfg.GoBin
	if goBin == "" {
		var err error
		if goBin, err = exec.LookPath("go"); err != nil {
			return nil, fmt.Errorf("find the go toolchain: %w", err)
		}
	}
	if cfg.CacheDir != "" {
		if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
			return nil, fmt.Errorf("create build cache: %w", err)
		}
	}
	if cfg.WorkDir != "" {
		if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
			return nil, fmt.Errorf("create work directory: %w", err)
		}
	}

	version, err := detectGoVersion(goBin)
	if err != nil {
		return nil, err
	}

	return &Runner{
		cfg:       cfg,
		goBin:     goBin,
		goVersion: version,
		log:       cfg.Logger,
		sem:       make(chan struct{}, cfg.MaxParallel),
		runs:      map[string]*Run{},
	}, nil
}

// GoVersion is the language version written into each sandbox go.mod.
//
// It mirrors the toolchain actually installed rather than a version pinned in
// source. Declaring a newer version would make every run start with a
// toolchain switch, which needs the network and fails with GOPROXY=off.
func (r *Runner) GoVersion() string { return r.goVersion }

// detectGoVersion asks the toolchain what language version it implements.
//
// Both the probe and the sandbox run with GOTOOLCHAIN=local and outside any
// module, so the answer is the version the go binary can serve on its own.
// Probing inside a module whose go.mod names a newer version would switch
// toolchains and report that newer version instead - and then every sandbox
// would be built with a go.mod its own toolchain refuses to load.
func detectGoVersion(goBin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, goBin, "env", "GOVERSION")
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off")

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read GOVERSION from %s: %w", goBin, err)
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "go")
	if v == "" {
		return "", fmt.Errorf("%s reported an empty GOVERSION", goBin)
	}
	// A devel toolchain reports something like "devel go1.27-abc"; fall back
	// to the language version it claims.
	if i := strings.IndexAny(v, " -+"); i > 0 {
		v = v[:i]
	}
	return v, nil
}

// Start launches a run and returns immediately. The caller follows the run's
// event stream or waits for its result.
func (r *Runner) Start(ctx context.Context, taskID string, attemptID int64, spec Spec) (*Run, error) {
	if len(spec.Files) == 0 {
		return nil, errors.New("nothing to run: no files were submitted")
	}

	// The run outlives the HTTP request that created it: a page reload must
	// not kill a compile in progress.
	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))

	run := newRun(newID(), taskID, attemptID, time.Now(), cancel)

	r.mu.Lock()
	r.evictLocked()
	r.runs[run.ID] = run
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel(nil)
		run.finish(r.guard(runCtx, run, spec))
	}()
	return run, nil
}

// Run returns a run by id.
func (r *Runner) Run(id string) (*Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	return run, ok
}

// Shutdown cancels every live run and waits for the sandboxes to be removed.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	for _, run := range r.runs {
		run.cancel(errors.New("server is shutting down"))
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("runner shutdown: %w", ctx.Err())
	}
}

func (r *Runner) evictLocked() {
	cutoff := time.Now().Add(-retainFinished)
	for id, run := range r.runs {
		run.mu.Lock()
		stale := run.finished && run.endedAt.Before(cutoff)
		run.mu.Unlock()
		if stale {
			delete(r.runs, id)
		}
	}
}

// guard turns a panic in the harness into an ordinary failed run. Without it a
// bug here would take the whole server down mid-session.
func (r *Runner) guard(ctx context.Context, run *Run, spec Spec) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("run panicked", "run", run.ID, "task", run.TaskID,
				"panic", p, "stack", string(debug.Stack()))
			res = Result{Outcome: OutcomeError, Message: fmt.Sprintf("internal error in the runner: %v", p)}
		}
	}()
	return r.execute(ctx, run, spec)
}

func (r *Runner) execute(ctx context.Context, run *Run, spec Spec) Result {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return canceledResult(ctx)
	}

	ws, err := newWorkspace(r.cfg.WorkDir, "drill/"+sanitiseModule(spec.TaskID), r.goVersion, spec)
	if err != nil {
		return Result{Outcome: OutcomeError, Message: err.Error()}
	}
	// Deferred before any work so that it also runs if anything below panics.
	defer ws.Cleanup()

	hardCtx, cancelHard := context.WithTimeout(ctx, r.cfg.HardTimeout)
	defer cancelHard()

	args := []string{"test", "./...", "-json", "-count=1", "-timeout", r.cfg.RunTimeout.String()}
	if spec.Race {
		args = append(args, "-race")
	}

	cmd := exec.CommandContext(hardCtx, r.goBin, args...)
	cmd.Dir = ws.dir
	cmd.Env = r.env()
	isolateProcessGroup(cmd)
	// CommandContext would otherwise kill only the go process, leaving the
	// compiled test binary running.
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	// Even after SIGKILL, Wait blocks while any process still holds the write
	// end of the pipes. WaitDelay gives up on the output instead of hanging.
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{Outcome: OutcomeError, Message: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{Outcome: OutcomeError, Message: err.Error()}
	}

	run.append(Event{Kind: KindLog, Stream: "harness",
		Text: fmt.Sprintf("$ go %s\n", strings.Join(args, " "))})

	if err := cmd.Start(); err != nil {
		return Result{Outcome: OutcomeError, Message: fmt.Sprintf("start go test: %v", err)}
	}

	var (
		wg  sync.WaitGroup
		acc = &accumulator{}
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.readJSON(run, acc, stdout)
	}()
	go func() {
		defer wg.Done()
		// Anything the go command itself writes - a toolchain complaint, a
		// vet diagnostic - never appears in the JSON stream.
		r.readRaw(run, acc, stderr)
	}()
	wg.Wait()

	waitErr := cmd.Wait()
	return acc.result(ctx, hardCtx, waitErr)
}

func (r *Runner) env() []string {
	// A deliberately small environment: the sandbox must not inherit the
	// developer's GOFLAGS, module settings or proxies.
	env := []string{
		"GOPROXY=off",       // hermetic: a submission cannot pull in dependencies
		"GOFLAGS=-mod=mod",  // the generated go.mod has no requirements to verify
		"GOTOOLCHAIN=local", // go.mod already declares the installed version
		"GOWORK=off",        // ignore any workspace file above the temp dir
	}
	if r.cfg.CacheDir != "" {
		env = append(env, "GOCACHE="+r.cfg.CacheDir)
	}
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "GOPATH", "GOMODCACHE", "USER", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func (r *Runner) readJSON(run *Run, acc *accumulator, rc io.Reader) {
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for sc.Scan() {
		line := sc.Bytes()
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Not JSON: pass it through rather than swallow it.
			run.append(Event{Kind: KindLog, Stream: "stdout", Text: string(line) + "\n"})
			acc.noteRaw(string(line))
			continue
		}
		acc.note(ev)

		if ev.Output != "" {
			run.append(Event{Kind: KindLog, Stream: "stdout", Text: ev.Output})
		}
		switch ev.Action {
		case "run", "pass", "fail", "skip":
			run.append(Event{Kind: KindTest, Action: ev.Action,
				Package: ev.Package, Test: ev.Test, Elapsed: ev.Elapsed})
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		run.append(Event{Kind: KindLog, Stream: "harness",
			Text: fmt.Sprintf("reading test output: %v\n", err)})
	}
}

func (r *Runner) readRaw(run *Run, acc *accumulator, rc io.Reader) {
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		acc.noteRaw(line)
		run.append(Event{Kind: KindLog, Stream: "stderr", Text: line + "\n"})
	}
}

func canceledResult(ctx context.Context) Result {
	if errors.Is(context.Cause(ctx), errCanceledByUser) {
		return Result{Outcome: OutcomeCanceled, Message: "run canceled"}
	}
	return Result{Outcome: OutcomeCanceled, Message: contextMessage(ctx)}
}

func contextMessage(ctx context.Context) string {
	if cause := context.Cause(ctx); cause != nil {
		return cause.Error()
	}
	return "canceled"
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; a run id is
		// not worth a second error path.
		panic(fmt.Sprintf("generate run id: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func sanitiseModule(taskID string) string {
	if taskID == "" {
		return "submission"
	}
	return taskID
}
