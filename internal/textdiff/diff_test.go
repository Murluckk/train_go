package textdiff

import (
	"strings"
	"testing"
)

// render turns a diff into a compact string so that expectations read like
// the side-by-side view they describe.
func render(d FileDiff) string {
	var b strings.Builder
	for _, r := range d.Rows {
		switch r.Op {
		case OpEqual:
			b.WriteString("  " + r.Left + "\n")
		case OpChange:
			b.WriteString("~ " + r.Left + " | " + r.Right + "\n")
		case OpInsert:
			b.WriteString("+ " + r.Right + "\n")
		case OpDelete:
			b.WriteString("- " + r.Left + "\n")
		}
	}
	return b.String()
}

func TestLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		left, right string
		want        string
		added       int
		removed     int
	}{
		{
			name:  "identical",
			left:  "a\nb\n",
			right: "a\nb\n",
			want:  "  a\n  b\n",
		},
		{
			name:    "one line changed",
			left:    "a\nb\nc\n",
			right:   "a\nB\nc\n",
			want:    "  a\n~ b | B\n  c\n",
			added:   1,
			removed: 1,
		},
		{
			name:  "insertion",
			left:  "a\nc\n",
			right: "a\nb\nc\n",
			want:  "  a\n+ b\n  c\n",
			added: 1,
		},
		{
			name:    "deletion",
			left:    "a\nb\nc\n",
			right:   "a\nc\n",
			want:    "  a\n- b\n  c\n",
			removed: 1,
		},
		{
			name:  "empty left",
			left:  "",
			right: "a\nb\n",
			want:  "+ a\n+ b\n",
			added: 2,
		},
		{
			name:    "empty right",
			left:    "a\nb\n",
			right:   "",
			want:    "- a\n- b\n",
			removed: 2,
		},
		{
			name:  "both sides empty",
			left:  "",
			right: "",
			want:  "",
		},
		{
			name:    "replacement block pairs up",
			left:    "head\nx1\nx2\ntail\n",
			right:   "head\ny1\ny2\ntail\n",
			want:    "  head\n~ x1 | y1\n~ x2 | y2\n  tail\n",
			added:   2,
			removed: 2,
		},
		{
			name:    "uneven replacement leaves the extra line alone",
			left:    "head\nx1\ntail\n",
			right:   "head\ny1\ny2\ntail\n",
			want:    "  head\n~ x1 | y1\n+ y2\n  tail\n",
			added:   2,
			removed: 1,
		},
		{
			name:  "no trailing newline",
			left:  "a",
			right: "a",
			want:  "  a\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Lines(tt.left, tt.right)
			if r := render(got); r != tt.want {
				t.Errorf("diff =\n%s\nwant\n%s", r, tt.want)
			}
			if got.Added != tt.added || got.Removed != tt.removed {
				t.Errorf("added = %d removed = %d, want %d and %d", got.Added, got.Removed, tt.added, tt.removed)
			}
			if want := tt.left == tt.right; got.Identical != want {
				t.Errorf("Identical = %v, want %v", got.Identical, want)
			}
		})
	}
}

func TestLineNumbers(t *testing.T) {
	t.Parallel()
	d := Lines("a\nb\nc\n", "a\nx\ny\nc\n")

	for _, r := range d.Rows {
		switch r.Op {
		case OpEqual, OpChange:
			if r.LeftNum == 0 || r.RightNum == 0 {
				t.Errorf("row %+v should have numbers on both sides", r)
			}
		case OpInsert:
			if r.LeftNum != 0 || r.RightNum == 0 {
				t.Errorf("insert row %+v should only number the right side", r)
			}
		case OpDelete:
			if r.RightNum != 0 || r.LeftNum == 0 {
				t.Errorf("delete row %+v should only number the left side", r)
			}
		}
	}

	// The numbers must be the real line numbers in each file, not row indices.
	last := d.Rows[len(d.Rows)-1]
	if last.LeftNum != 3 || last.RightNum != 4 {
		t.Errorf("last row = %+v, want left line 3 and right line 4", last)
	}
}

func TestLinesOnLargeInput(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("line\n", maxLines+1)
	d := Lines(big, big+"extra\n")
	if len(d.Rows) != 1 {
		t.Errorf("rows = %d, want a single placeholder row for an oversized file", len(d.Rows))
	}
}

func TestFiles(t *testing.T) {
	t.Parallel()
	left := map[string]string{"a.go": "package a\n", "only_mine.go": "package a\n"}
	right := map[string]string{"a.go": "package b\n", "only_theirs.go": "package a\n"}

	got := Files(left, right)
	if len(got) != 3 {
		t.Fatalf("got %d diffs, want 3", len(got))
	}
	if got[0].Path != "a.go" || got[1].Path != "only_mine.go" || got[2].Path != "only_theirs.go" {
		t.Errorf("paths are not sorted: %v", []string{got[0].Path, got[1].Path, got[2].Path})
	}
	if got[1].OnlyIn != "left" {
		t.Errorf("only_mine.go OnlyIn = %q, want left", got[1].OnlyIn)
	}
	if got[2].OnlyIn != "right" {
		t.Errorf("only_theirs.go OnlyIn = %q, want right", got[2].OnlyIn)
	}
}

func TestGofmtDiff(t *testing.T) {
	t.Parallel()

	badly := "package a\n\nfunc F()  int  {\nreturn 1\n}\n"
	d, err := GofmtDiff("a.go", badly)
	if err != nil {
		t.Fatalf("GofmtDiff() error = %v", err)
	}
	if d.Identical {
		t.Error("badly formatted code should differ from gofmt output")
	}
	if d.Path != "a.go" {
		t.Errorf("Path = %q", d.Path)
	}

	clean := "package a\n\nfunc F() int {\n\treturn 1\n}\n"
	d, err = GofmtDiff("a.go", clean)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Identical {
		t.Errorf("gofmt-clean code reported as different:\n%s", render(d))
	}
}

func TestGofmtDiffOnBrokenCode(t *testing.T) {
	t.Parallel()
	if _, err := GofmtDiff("a.go", "package a\n\nfunc F() int { return }}\n"); err == nil {
		t.Error("GofmtDiff() should fail on code that does not parse")
	}
}
