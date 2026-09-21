package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"drill/internal/catalog"
)

// Spec is everything needed to run one submission.
type Spec struct {
	TaskID string
	// Files are the editor buffers, keyed by path relative to the module root.
	Files map[string]string
	// Tests come from the task's tests/ directory and are never shown to the
	// user.
	Tests map[string]string
	// Race controls the -race flag.
	Race bool
}

// workspace is a throwaway module on disk.
type workspace struct {
	dir string
}

// newWorkspace materialises the submission in a fresh temporary directory.
//
// User files are written first and the task's tests second, so that a
// submission can never shadow a test file. The path validation above is the
// real guard; this ordering is the belt to its braces.
func newWorkspace(parent, module, goVersion string, spec Spec) (*workspace, error) {
	dir, err := os.MkdirTemp(parent, "drill-run-")
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	ws := &workspace{dir: dir}

	gomod := fmt.Sprintf("module %s\n\ngo %s\n", module, goVersion)
	if err := ws.write("go.mod", gomod); err != nil {
		ws.Cleanup()
		return nil, err
	}

	for path, content := range spec.Files {
		if err := catalog.ValidateUserPath(path); err != nil {
			ws.Cleanup()
			return nil, fmt.Errorf("submitted file: %w", err)
		}
		if err := ws.write(path, content); err != nil {
			ws.Cleanup()
			return nil, err
		}
	}

	for path, content := range spec.Tests {
		if err := validateTaskPath(path); err != nil {
			ws.Cleanup()
			return nil, fmt.Errorf("task test file: %w", err)
		}
		if err := ws.write(path, content); err != nil {
			ws.Cleanup()
			return nil, err
		}
	}

	return ws, nil
}

func (w *workspace) write(rel, content string) error {
	full := filepath.Join(w.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// Cleanup removes the sandbox. Callers defer it before doing any work so that
// it runs on the panic path too.
func (w *workspace) Cleanup() {
	if w == nil || w.dir == "" {
		return
	}
	_ = os.RemoveAll(w.dir)
}

// validateTaskPath is the same guard as for editor buffers, minus the .go
// restriction: a task may ship a testdata fixture.
func validateTaskPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("empty path")
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q must be relative", p)
	case p == "go.mod" || p == "go.sum":
		return fmt.Errorf("%q is owned by the runner", p)
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("%q must not contain %q segments", p, seg)
		}
	}
	return nil
}
