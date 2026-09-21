// Package textdiff produces a line diff for the divergence journal.
//
// It is a hand-rolled LCS rather than a dependency: the inputs are two small
// Go files, the output feeds a side-by-side view whose exact shape this
// package controls, and pulling in a diff library for that would be a poor
// trade.
package textdiff

import (
	"go/format"
	"slices"
	"strings"
)

// Op describes what happened to one row of the side-by-side view.
type Op string

const (
	// OpEqual means both sides carry the same line.
	OpEqual Op = "equal"
	// OpChange pairs a removed line with the line that replaced it.
	OpChange Op = "change"
	// OpInsert is a line present only on the right.
	OpInsert Op = "insert"
	// OpDelete is a line present only on the left.
	OpDelete Op = "delete"
)

// Row is one line of the side-by-side rendering. Line numbers are 1-based and
// zero where that side has no line.
type Row struct {
	Op       Op     `json:"op"`
	LeftNum  int    `json:"left_num"`
	RightNum int    `json:"right_num"`
	Left     string `json:"left"`
	Right    string `json:"right"`
}

// FileDiff is the comparison of one file.
type FileDiff struct {
	Path      string `json:"path"`
	Rows      []Row  `json:"rows"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Identical bool   `json:"identical"`
	// OnlyIn is "left" or "right" when the file exists on one side only.
	OnlyIn string `json:"only_in,omitempty"`
}

// maxLines bounds the quadratic part of the algorithm. Task solutions are tens
// of lines; anything past this is not worth aligning line by line.
const maxLines = 4000

// Lines compares two texts.
func Lines(left, right string) FileDiff {
	d := FileDiff{Identical: left == right}
	a, b := splitLines(left), splitLines(right)

	if d.Identical {
		for i, line := range a {
			d.Rows = append(d.Rows, Row{Op: OpEqual, LeftNum: i + 1, RightNum: i + 1, Left: line, Right: line})
		}
		return d
	}
	if len(a) > maxLines || len(b) > maxLines {
		d.Rows = []Row{{Op: OpChange, Left: "<файл слишком большой для построчного сравнения>",
			Right: "<файл слишком большой для построчного сравнения>"}}
		return d
	}

	// Trimming the shared head and tail keeps the DP table small for the
	// common case of a few edits in the middle of an otherwise equal file.
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}

	for i := range head {
		d.Rows = append(d.Rows, Row{Op: OpEqual, LeftNum: i + 1, RightNum: i + 1, Left: a[i], Right: b[i]})
	}

	mid := align(a[head:len(a)-tail], b[head:len(b)-tail], head)
	d.Rows = append(d.Rows, mid...)

	for i := range tail {
		la, lb := len(a)-tail+i, len(b)-tail+i
		d.Rows = append(d.Rows, Row{Op: OpEqual, LeftNum: la + 1, RightNum: lb + 1, Left: a[la], Right: b[lb]})
	}

	for _, r := range d.Rows {
		switch r.Op {
		case OpInsert:
			d.Added++
		case OpDelete:
			d.Removed++
		case OpChange:
			d.Added++
			d.Removed++
		}
	}
	return d
}

// align runs the LCS over the differing middle and pairs adjacent
// delete/insert runs into change rows, which is what makes the side-by-side
// view readable.
func align(a, b []string, offset int) []Row {
	lcs := longestCommon(a, b)

	var (
		rows           []Row
		i, j           int
		pendingDeletes []Row
		pendingInserts []Row
	)

	flush := func() {
		n := min(len(pendingDeletes), len(pendingInserts))
		for k := range n {
			rows = append(rows, Row{
				Op:       OpChange,
				LeftNum:  pendingDeletes[k].LeftNum,
				RightNum: pendingInserts[k].RightNum,
				Left:     pendingDeletes[k].Left,
				Right:    pendingInserts[k].Right,
			})
		}
		rows = append(rows, pendingDeletes[n:]...)
		rows = append(rows, pendingInserts[n:]...)
		pendingDeletes, pendingInserts = nil, nil
	}

	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			flush()
			rows = append(rows, Row{Op: OpEqual,
				LeftNum: offset + i + 1, RightNum: offset + j + 1, Left: a[i], Right: b[j]})
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			pendingInserts = append(pendingInserts, Row{Op: OpInsert,
				RightNum: offset + j + 1, Right: b[j]})
			j++
		default:
			pendingDeletes = append(pendingDeletes, Row{Op: OpDelete,
				LeftNum: offset + i + 1, Left: a[i]})
			i++
		}
	}
	flush()
	return rows
}

// longestCommon builds the LCS length table, indexed from the end so that
// lcs[i][j] is the length for a[i:] and b[j:].
func longestCommon(a, b []string) [][]int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	return table
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// Files compares two sets of files keyed by path, including files that exist
// on only one side.
func Files(left, right map[string]string) []FileDiff {
	paths := map[string]bool{}
	for p := range left {
		paths[p] = true
	}
	for p := range right {
		paths[p] = true
	}

	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	slices.Sort(ordered)

	out := make([]FileDiff, 0, len(ordered))
	for _, p := range ordered {
		l, okL := left[p]
		r, okR := right[p]
		d := Lines(l, r)
		d.Path = p
		switch {
		case !okR:
			d.OnlyIn = "left"
		case !okL:
			d.OnlyIn = "right"
		}
		out = append(out, d)
	}
	return out
}

// GofmtDiff compares the source with what gofmt would produce.
//
// Automatic formatting is switched off in the editor on purpose - formatting
// by hand is part of the practice - so this belongs in the review afterwards,
// not in the editor.
func GofmtDiff(path, src string) (FileDiff, error) {
	formatted, err := format.Source([]byte(src))
	if err != nil {
		// Unparsable code has no meaningful gofmt output; the compiler will
		// have said something more useful already.
		return FileDiff{}, err
	}
	d := Lines(src, string(formatted))
	d.Path = path
	return d, nil
}
