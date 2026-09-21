package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Layout of a task directory.
const (
	manifestName = "task.yaml"
	readmeName   = "README.md"
	starterDir   = "starter"
	testsDir     = "tests"
	solutionDir  = "solution"
)

// maxFileSize guards against a stray binary landing in a task directory and
// being loaded into memory and then into the browser.
const maxFileSize = 256 << 10

type manifest struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Topic       string `yaml:"topic"`
	Difficulty  int    `yaml:"difficulty"`
	EstimateMin int    `yaml:"estimate_minutes"`
	Race        *bool  `yaml:"race"`
}

// Load scans root and returns a snapshot. A directory that fails validation is
// recorded in the snapshot's error list instead of failing the whole scan: one
// broken task must not take the trainer down.
func Load(root string) (*Snapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read tasks directory: %w", err)
	}

	var (
		tasks []*Task
		errs  []LoadError
		seen  = map[string]string{}
	)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		t, err := LoadTask(dir)
		if err != nil {
			errs = append(errs, LoadError{Dir: e.Name(), Err: err.Error()})
			continue
		}
		if prev, dup := seen[t.ID]; dup {
			errs = append(errs, LoadError{Dir: e.Name(),
				Err: fmt.Sprintf("duplicate task id %q, already defined in %s", t.ID, prev)})
			continue
		}
		seen[t.ID] = e.Name()
		tasks = append(tasks, t)
	}

	fp, err := Fingerprint(root)
	if err != nil {
		return nil, err
	}
	return NewSnapshot(tasks, errs, fp), nil
}

// LoadTask reads a single task directory.
func LoadTask(dir string) (*Task, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestName, err)
	}

	var m manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a misspelled key is a mistake, not something to ignore
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestName, err)
	}

	t := &Task{
		ID:          m.ID,
		Title:       m.Title,
		Topic:       m.Topic,
		Difficulty:  m.Difficulty,
		EstimateMin: m.EstimateMin,
		Race:        m.Race == nil || *m.Race,
		Dir:         dir,
	}

	readme, err := os.ReadFile(filepath.Join(dir, readmeName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", readmeName, err)
	}
	t.Readme = string(readme)

	if t.Starter, err = readTree(filepath.Join(dir, starterDir)); err != nil {
		return nil, fmt.Errorf("%s: %w", starterDir, err)
	}
	if t.Tests, err = readTree(filepath.Join(dir, testsDir)); err != nil {
		return nil, fmt.Errorf("%s: %w", testsDir, err)
	}
	if t.Solution, err = readTree(filepath.Join(dir, solutionDir)); err != nil {
		return nil, fmt.Errorf("%s: %w", solutionDir, err)
	}

	if err := t.validate(filepath.Base(dir)); err != nil {
		return nil, err
	}
	t.ContentHash = hashTask(t)
	return t, nil
}

var idPattern = func(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

func (t *Task) validate(dirName string) error {
	var errs []error

	if !idPattern(t.ID) {
		errs = append(errs, fmt.Errorf("id %q must be lowercase letters, digits and dashes", t.ID))
	}
	// Directory name and id must agree, otherwise renaming a directory would
	// silently orphan every attempt recorded against the old id.
	if t.ID != dirName {
		errs = append(errs, fmt.Errorf("id %q does not match directory name %q", t.ID, dirName))
	}
	if strings.TrimSpace(t.Title) == "" {
		errs = append(errs, errors.New("title must not be empty"))
	}
	if strings.TrimSpace(t.Topic) == "" {
		errs = append(errs, errors.New("topic must not be empty"))
	}
	if t.Difficulty < 1 || t.Difficulty > 3 {
		errs = append(errs, fmt.Errorf("difficulty must be 1..3, got %d", t.Difficulty))
	}
	if t.EstimateMin <= 0 {
		errs = append(errs, fmt.Errorf("estimate_minutes must be positive, got %d", t.EstimateMin))
	}
	if strings.TrimSpace(t.Readme) == "" {
		errs = append(errs, errors.New("README.md must not be empty"))
	}
	if len(t.Starter) == 0 {
		errs = append(errs, errors.New("starter/ must contain at least one file"))
	}
	if !slices.ContainsFunc(t.Tests, func(f File) bool { return strings.HasSuffix(f.Path, "_test.go") }) {
		errs = append(errs, errors.New("tests/ must contain at least one _test.go file"))
	}
	if len(t.Solution) == 0 {
		errs = append(errs, errors.New("solution/ must contain the reference implementation"))
	}

	// The runner writes the editor buffers first and the tests second. A
	// collision would let a starter file quietly shadow a test, so it is
	// rejected here rather than discovered as a mysteriously passing run.
	testPaths := make(map[string]bool, len(t.Tests))
	for _, f := range t.Tests {
		testPaths[f.Path] = true
	}
	for _, f := range t.Starter {
		if testPaths[f.Path] {
			errs = append(errs, fmt.Errorf("starter/%s collides with a file in tests/", f.Path))
		}
		if strings.HasSuffix(f.Path, "_test.go") {
			errs = append(errs, fmt.Errorf("starter/%s: the editor must not hold test files", f.Path))
		}
	}
	for _, group := range [][]File{t.Starter, t.Tests, t.Solution} {
		for _, f := range group {
			if f.Path == "go.mod" || f.Path == "go.sum" {
				errs = append(errs, fmt.Errorf("%s is generated by the runner and must not be checked in", f.Path))
			}
		}
	}

	return errors.Join(errs...)
}

// readTree reads every regular file under dir, returning paths relative to it
// with forward slashes. A missing directory yields no files and no error.
func readTree(dir string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Symlinks are skipped: a task directory should be self contained.
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileSize {
			return fmt.Errorf("%s is %d bytes, over the %d byte limit", d.Name(), info.Size(), maxFileSize)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, File{Path: filepath.ToSlash(rel), Content: string(body)})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// hashTask produces a stable digest of everything that can make a task behave
// differently. It is how the UI can say "this task changed since you last
// solved it".
func hashTask(t *Task) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n%d\n%d\n%t\n", t.ID, t.Title, t.Topic, t.Difficulty, t.EstimateMin, t.Race)
	h.Write([]byte(t.Readme))
	for _, group := range [][]File{t.Starter, t.Tests, t.Solution} {
		for _, f := range group {
			fmt.Fprintf(h, "\x00%s\x00%s", f.Path, f.Content)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Fingerprint summarises the directory tree cheaply, from names, sizes and
// modification times only. Polling this beats a filesystem watcher here: it is
// a few hundred stat calls, it needs no dependency, and it is immune to the
// create-rename-chmod dance editors perform on every save.
func Fingerprint(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != root {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ValidateUserPath rejects editor buffer names that could escape the sandbox
// or overwrite files the runner owns.
func ValidateUserPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q must be relative", p)
	case strings.Contains(p, `\`):
		return fmt.Errorf("%q must use forward slashes", p)
	}
	clean := path.Clean(p)
	if clean != p {
		return fmt.Errorf("%q is not a clean path (want %q)", p, clean)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("%q must not contain %q segments", p, seg)
		}
	}
	if clean == "go.mod" || clean == "go.sum" {
		return fmt.Errorf("%q is owned by the runner", p)
	}
	if strings.HasSuffix(clean, "_test.go") {
		return fmt.Errorf("%q: test files come from the task, not from the editor", p)
	}
	if !strings.HasSuffix(clean, ".go") {
		return fmt.Errorf("%q: only .go files can be submitted", p)
	}
	return nil
}

// FileMap turns a file list into a path-keyed map.
func FileMap(files []File) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Path] = f.Content
	}
	return out
}

// SortedPaths returns the paths of a file map in stable order.
func SortedPaths(files map[string]string) []string {
	out := slices.Collect(maps.Keys(files))
	sort.Strings(out)
	return out
}
