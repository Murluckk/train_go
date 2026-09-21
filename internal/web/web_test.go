package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAssetsAreEmbedded(t *testing.T) {
	t.Parallel()
	h := newHandler(t)

	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "<title>drill</title>"},
		{path: "/app.js", contentType: "javascript", contains: "renderTaskScreen"},
		{path: "/util.js", contentType: "javascript", contains: "export function markdown"},
		{path: "/task.js", contentType: "javascript", contains: "class TaskScreen"},
		{path: "/stats.js", contentType: "javascript", contains: "export async function renderStats"},
		{path: "/editor.js", contentType: "javascript", contains: "createEditor"},
		{path: "/app.css", contentType: "text/css", contains: ".cm-editor"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			rec := get(t, h, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want it to contain %q; ES modules need the right type", ct, tt.contentType)
			}
			if !strings.Contains(rec.Body.String(), tt.contains) {
				t.Errorf("body does not contain %q", tt.contains)
			}
		})
	}
}

// importMap extracts and parses the page's import map. Parsing it rather than
// grepping also catches a malformed map, which breaks every bare import on
// the page with no error the user would understand.
func importMap(t *testing.T, h http.Handler) map[string]string {
	t.Helper()
	body := get(t, h, "/").Body.String()

	open := strings.Index(body, `<script type="importmap">`)
	if open < 0 {
		t.Fatal("the import map is missing; CodeMirror cannot resolve its bare imports without it")
	}
	rest := body[open+len(`<script type="importmap">`):]
	closeAt := strings.Index(rest, "</script>")
	if closeAt < 0 {
		t.Fatal("the import map script is not closed")
	}

	var parsed struct {
		Imports map[string]string `json:"imports"`
	}
	if err := json.Unmarshal([]byte(rest[:closeAt]), &parsed); err != nil {
		t.Fatalf("the import map is not valid JSON: %v", err)
	}
	if len(parsed.Imports) == 0 {
		t.Fatal("the import map is empty")
	}
	return parsed.Imports
}

// The trainer exists to remove editor assistance, so the completion package
// must not be reachable at all. Configuring it off would be one refactor away
// from coming back.
func TestAutocompleteIsNotShipped(t *testing.T) {
	t.Parallel()
	imports := importMap(t, newHandler(t))

	for specifier := range imports {
		if strings.HasPrefix(specifier, "@codemirror/autocomplete") {
			t.Errorf("the import map exposes %q; completion must not be loadable", specifier)
		}
	}
	for _, pkg := range []string{"@codemirror/state", "@codemirror/view", "@codemirror/language", "@codemirror/commands"} {
		if imports[pkg] == "" {
			t.Errorf("the import map does not pin %s", pkg)
		}
	}
}

func TestEveryImportMapEntryIsPinned(t *testing.T) {
	t.Parallel()

	for specifier, url := range importMap(t, newHandler(t)) {
		if !strings.HasPrefix(url, "https://") {
			t.Errorf("%s: %q is not an https URL", specifier, url)
			continue
		}
		// A floating version would change the editor under the user on some
		// random morning, and two copies of @codemirror/state break it
		// outright.
		version := strings.LastIndex(url, "@")
		if version < len("https://") || strings.Contains(url, "@latest") {
			t.Errorf("%s: %q is not pinned to an exact version", specifier, url)
		}
	}
}

// basicSetup is what makes a hand-rolled CodeMirror quietly grow completion
// again, so the editor must never reach for it.
func TestEditorDoesNotUseBasicSetup(t *testing.T) {
	t.Parallel()
	body := stripComments(get(t, newHandler(t), "/editor.js").Body.String())

	if strings.Contains(body, "basicSetup") {
		t.Error("editor.js uses basicSetup, which bundles autocompletion and linting")
	}
	if strings.Contains(body, "autocompletion") {
		t.Error("editor.js references autocompletion")
	}
	if !strings.Contains(body, "plainEditor") {
		t.Error("editor.js has no textarea fallback; an offline session would have no editor at all")
	}
}

// stripComments removes // and /* */ comments so that a test about what the
// code does is not fooled by a comment explaining what it deliberately does
// not do.
func stripComments(src string) string {
	var (
		out   strings.Builder
		block bool
	)
	for line := range strings.SplitSeq(src, "\n") {
		for block {
			end := strings.Index(line, "*/")
			if end < 0 {
				line = ""
				break
			}
			line = line[end+2:]
			block = false
		}
		if i := strings.Index(line, "/*"); i >= 0 {
			block = true
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestUnknownPathServesTheShell(t *testing.T) {
	t.Parallel()
	// Hash routing means the server only ever sees "/", but a stray deep link
	// should still land on the app rather than a 404 page.
	rec := get(t, newHandler(t), "/task/slices-01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<title>drill</title>") {
		t.Error("an unknown path should serve the shell")
	}
}

func TestShellIsNotCached(t *testing.T) {
	t.Parallel()
	rec := get(t, newHandler(t), "/")
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache; the page ships with the binary and a stale copy is pure confusion", cc)
	}
}
