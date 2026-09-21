package config

import (
	"errors"
	"flag"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) Getenv {
	return func(k string) string { return pairs[k] }
}

func TestParse(t *testing.T) {
	t.Parallel()

	base := map[string]string{"HOME": "/home/tester"}

	tests := []struct {
		name    string
		args    []string
		environ map[string]string
		want    func(t *testing.T, c *Config)
		wantErr string
	}{
		{
			name:    "defaults",
			environ: base,
			want: func(t *testing.T, c *Config) {
				if c.Addr != "127.0.0.1:8080" {
					t.Errorf("Addr = %q, want loopback default", c.Addr)
				}
				if want := filepath.Join("/home/tester", ".local", "share", "drill", "drill.db"); c.DBPath != want {
					t.Errorf("DBPath = %q, want %q", c.DBPath, want)
				}
				if want := filepath.Join("/home/tester", ".cache", "drill", "gocache"); c.CacheDir != want {
					t.Errorf("CacheDir = %q, want %q", c.CacheDir, want)
				}
				if c.LogLevel != slog.LevelInfo {
					t.Errorf("LogLevel = %v, want info", c.LogLevel)
				}
				if !filepath.IsAbs(c.TasksDir) {
					t.Errorf("TasksDir = %q, want absolute", c.TasksDir)
				}
			},
		},
		{
			name:    "xdg overrides home",
			environ: map[string]string{"HOME": "/home/tester", "XDG_DATA_HOME": "/data", "XDG_CACHE_HOME": "/cache"},
			want: func(t *testing.T, c *Config) {
				if want := filepath.Join("/data", "drill", "drill.db"); c.DBPath != want {
					t.Errorf("DBPath = %q, want %q", c.DBPath, want)
				}
				if want := filepath.Join("/cache", "drill", "gocache"); c.CacheDir != want {
					t.Errorf("CacheDir = %q, want %q", c.CacheDir, want)
				}
			},
		},
		{
			name:    "environment is honoured",
			environ: map[string]string{"HOME": "/home/tester", "DRILL_ADDR": "127.0.0.1:9999", "DRILL_RUN_TIMEOUT": "5s", "DRILL_MAX_PARALLEL": "7"},
			want: func(t *testing.T, c *Config) {
				if c.Addr != "127.0.0.1:9999" {
					t.Errorf("Addr = %q", c.Addr)
				}
				if c.RunTimeout != 5*time.Second {
					t.Errorf("RunTimeout = %s", c.RunTimeout)
				}
				if c.MaxParallel != 7 {
					t.Errorf("MaxParallel = %d", c.MaxParallel)
				}
			},
		},
		{
			name:    "flag beats environment",
			args:    []string{"-addr", "127.0.0.1:1234"},
			environ: map[string]string{"HOME": "/home/tester", "DRILL_ADDR": "127.0.0.1:9999"},
			want: func(t *testing.T, c *Config) {
				if c.Addr != "127.0.0.1:1234" {
					t.Errorf("Addr = %q, want the flag value", c.Addr)
				}
			},
		},
		{
			name:    "log level parsed",
			args:    []string{"-log-level", "debug"},
			environ: base,
			want: func(t *testing.T, c *Config) {
				if c.LogLevel != slog.LevelDebug {
					t.Errorf("LogLevel = %v, want debug", c.LogLevel)
				}
			},
		},
		{
			name:    "relative paths become absolute",
			args:    []string{"-tasks", "./t", "-db", "./x.db", "-cache", "./c"},
			environ: base,
			want: func(t *testing.T, c *Config) {
				for _, p := range []string{c.TasksDir, c.DBPath, c.CacheDir} {
					if !filepath.IsAbs(p) {
						t.Errorf("%q is not absolute", p)
					}
				}
			},
		},
		{
			name:    "hard timeout must exceed run timeout",
			args:    []string{"-run-timeout", "30s", "-hard-timeout", "10s"},
			environ: base,
			wantErr: "hard-timeout",
		},
		{
			name:    "zero parallelism rejected",
			args:    []string{"-max-parallel", "0"},
			environ: base,
			wantErr: "max-parallel",
		},
		{
			name:    "bad log level rejected",
			args:    []string{"-log-level", "screaming"},
			environ: base,
			wantErr: "log-level",
		},
		{
			name:    "bad log format rejected",
			args:    []string{"-log-format", "xml"},
			environ: base,
			wantErr: "log-format",
		},
		{
			name:    "positional argument rejected",
			args:    []string{"serve"},
			environ: base,
			wantErr: "unexpected positional argument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.args, env(tt.environ), io.Discard)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse() = %+v, want error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse() error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			tt.want(t, got)
		})
	}
}

func TestParseHelp(t *testing.T) {
	t.Parallel()
	_, err := Parse([]string{"-h"}, env(map[string]string{"HOME": "/home/tester"}), io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(-h) error = %v, want flag.ErrHelp", err)
	}
}

func TestAllValidationErrorsReported(t *testing.T) {
	t.Parallel()
	_, err := Parse([]string{"-max-parallel", "0", "-scan-interval", "0"},
		env(map[string]string{"HOME": "/home/tester"}), io.Discard)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"max-parallel", "scan-interval"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q; validation should report every problem at once", err, want)
		}
	}
}
