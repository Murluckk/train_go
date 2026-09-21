// Package config resolves runtime configuration from flags and environment.
//
// Precedence is flag > environment > default: every flag takes its default
// value from the corresponding DRILL_* variable, so an explicit flag always
// wins without any extra bookkeeping.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Config holds every knob the binary understands.
type Config struct {
	// Addr is the listen address. It defaults to loopback on purpose: the
	// service compiles and executes arbitrary code on request, so exposing
	// it on a LAN interface would be remote code execution.
	Addr string

	TasksDir string // directory scanned for task definitions
	DBPath   string // SQLite file
	CacheDir string // persistent GOCACHE shared by every test run
	WorkDir  string // parent of the per-run temporary sandboxes ("" = os.TempDir)

	GoBin string // go toolchain binary used by the runner

	// RunTimeout is handed to "go test -timeout". HardTimeout is the outer
	// context deadline that kills the whole process group when go test
	// itself is wedged; it must be comfortably larger so that, in the normal
	// case, go test wins the race and prints its goroutine dump.
	RunTimeout  time.Duration
	HardTimeout time.Duration

	MaxParallel   int
	ScanInterval  time.Duration
	ShutdownGrace time.Duration

	LogLevel  slog.Level
	LogFormat string // "text" or "json"

	// SkipSelfCheck skips the startup probe that compiles and runs a trivial
	// package. Only useful when the toolchain is known good and startup
	// latency matters.
	SkipSelfCheck bool
}

const (
	envPrefix        = "DRILL_"
	defaultAddr      = "127.0.0.1:8080"
	defaultTasksDir  = "./tasks"
	defaultRunTmo    = 30 * time.Second
	defaultHardTmo   = 90 * time.Second
	defaultScan      = 2 * time.Second
	defaultShutdown  = 10 * time.Second
	defaultLogFormat = "text"
)

// Getenv mirrors os.Getenv and exists so tests can supply a fake environment.
type Getenv func(string) string

// Parse builds a Config from command line arguments and the environment.
// args must not include the program name. A request for -h returns
// flag.ErrHelp, which callers should treat as a clean exit.
func Parse(args []string, getenv Getenv, out io.Writer) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	defDB, err := defaultDBPath(getenv)
	if err != nil {
		return nil, err
	}
	defCache, err := defaultCacheDir(getenv)
	if err != nil {
		return nil, err
	}

	fs := flag.NewFlagSet("drill", flag.ContinueOnError)
	fs.SetOutput(out)

	cfg := &Config{}
	var logLevel, logFormat string

	fs.StringVar(&cfg.Addr, "addr", envStr(getenv, "ADDR", defaultAddr),
		"listen address (keep it on loopback: runs arbitrary code)")
	fs.StringVar(&cfg.TasksDir, "tasks", envStr(getenv, "TASKS", defaultTasksDir),
		"directory containing task definitions")
	fs.StringVar(&cfg.DBPath, "db", envStr(getenv, "DB", defDB),
		"path to the SQLite database file")
	fs.StringVar(&cfg.CacheDir, "cache", envStr(getenv, "CACHE", defCache),
		"persistent GOCACHE for test runs (never put this in a temp dir)")
	fs.StringVar(&cfg.WorkDir, "work-dir", envStr(getenv, "WORK_DIR", ""),
		"parent directory for per-run sandboxes (default: system temp)")
	fs.StringVar(&cfg.GoBin, "go", envStr(getenv, "GO", ""),
		"go binary used to run tests (default: looked up in PATH)")
	fs.DurationVar(&cfg.RunTimeout, "run-timeout", envDur(getenv, "RUN_TIMEOUT", defaultRunTmo),
		"value passed to go test -timeout")
	fs.DurationVar(&cfg.HardTimeout, "hard-timeout", envDur(getenv, "HARD_TIMEOUT", defaultHardTmo),
		"outer deadline after which the process group is killed")
	fs.IntVar(&cfg.MaxParallel, "max-parallel", envInt(getenv, "MAX_PARALLEL", defaultParallel()),
		"maximum concurrent test runs")
	fs.DurationVar(&cfg.ScanInterval, "scan-interval", envDur(getenv, "SCAN_INTERVAL", defaultScan),
		"how often the tasks directory is rescanned")
	fs.DurationVar(&cfg.ShutdownGrace, "shutdown-grace", envDur(getenv, "SHUTDOWN_GRACE", defaultShutdown),
		"how long to wait for in-flight requests on shutdown")
	fs.BoolVar(&cfg.SkipSelfCheck, "skip-self-check", envStr(getenv, "SKIP_SELF_CHECK", "") != "",
		"skip the startup probe that verifies the sandbox can build and run Go")
	fs.StringVar(&logLevel, "log-level", envStr(getenv, "LOG_LEVEL", "info"),
		"debug, info, warn or error")
	fs.StringVar(&logFormat, "log-format", envStr(getenv, "LOG_FORMAT", defaultLogFormat),
		"text or json")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected positional argument %q", fs.Arg(0))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		return nil, fmt.Errorf("log-level: %w", err)
	}
	cfg.LogFormat = logFormat

	if err := cfg.finalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) finalize() error {
	var errs []error

	if c.Addr == "" {
		errs = append(errs, errors.New("addr must not be empty"))
	}
	if c.TasksDir == "" {
		errs = append(errs, errors.New("tasks must not be empty"))
	}
	if c.DBPath == "" {
		errs = append(errs, errors.New("db must not be empty"))
	}
	if c.CacheDir == "" {
		errs = append(errs, errors.New("cache must not be empty"))
	}
	if c.RunTimeout <= 0 {
		errs = append(errs, fmt.Errorf("run-timeout must be positive, got %s", c.RunTimeout))
	}
	if c.HardTimeout <= c.RunTimeout {
		errs = append(errs, fmt.Errorf("hard-timeout (%s) must exceed run-timeout (%s) so go test reports the timeout itself",
			c.HardTimeout, c.RunTimeout))
	}
	if c.MaxParallel < 1 {
		errs = append(errs, fmt.Errorf("max-parallel must be at least 1, got %d", c.MaxParallel))
	}
	if c.ScanInterval <= 0 {
		errs = append(errs, fmt.Errorf("scan-interval must be positive, got %s", c.ScanInterval))
	}
	if c.ShutdownGrace <= 0 {
		errs = append(errs, fmt.Errorf("shutdown-grace must be positive, got %s", c.ShutdownGrace))
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("log-format must be text or json, got %q", c.LogFormat))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, p := range []*string{&c.TasksDir, &c.DBPath, &c.CacheDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return fmt.Errorf("resolve %q: %w", *p, err)
		}
		*p = abs
	}
	return nil
}

// Logger builds the slog handler described by the configuration.
func (c *Config) Logger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func defaultParallel() int {
	// -race is memory hungry and there is exactly one user, so half the CPUs
	// is generous already.
	return max(1, runtime.NumCPU()/2)
}

func defaultDBPath(getenv Getenv) (string, error) {
	dir, err := xdgDir(getenv, "XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "drill", "drill.db"), nil
}

func defaultCacheDir(getenv Getenv) (string, error) {
	dir, err := xdgDir(getenv, "XDG_CACHE_HOME", ".cache")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "drill", "gocache"), nil
}

func xdgDir(getenv Getenv, env string, fallback ...string) (string, error) {
	if v := getenv(env); v != "" {
		return v, nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("cannot determine home directory (set %s): %w", env, err)
		}
	}
	return filepath.Join(append([]string{home}, fallback...)...), nil
}

func envStr(getenv Getenv, key, def string) string {
	if v := getenv(envPrefix + key); v != "" {
		return v
	}
	return def
}

func envInt(getenv Getenv, key string, def int) int {
	v := getenv(envPrefix + key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		// A malformed value is reported by the flag package when it parses
		// the same string as the flag default.
		return def
	}
	return n
}

func envDur(getenv Getenv, key string, def time.Duration) time.Duration {
	v := getenv(envPrefix + key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
