package solution

import (
	"slices"
	"strings"
	"testing"
)

func even(x int) bool { return x%2 == 0 }

func TestFilter(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		keep func(int) bool
		want []int
	}{
		{name: "nil остаётся nil", in: nil, keep: even, want: nil},
		{name: "пустой слайс", in: []int{}, keep: even, want: nil},
		{name: "ничего не подошло", in: []int{1, 3, 5}, keep: even, want: nil},
		{name: "подошло всё", in: []int{2, 4}, keep: even, want: []int{2, 4}},
		{name: "порядок сохраняется", in: []int{1, 2, 3, 4, 5, 6}, keep: even, want: []int{2, 4, 6}},
		{name: "предикат всегда false", in: []int{1, 2}, keep: func(int) bool { return false }, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Filter(tt.in, tt.keep)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Filter(%v) = %v, ожидалось %v", tt.in, got, tt.want)
			}
			if tt.want == nil && got != nil {
				t.Errorf("Filter(%v) = %#v (len=%d), ожидался именно nil", tt.in, got, len(got))
			}
		})
	}
}

func TestFilterIsGeneric(t *testing.T) {
	strs := Filter([]string{"alpha", "bo", "charlie"}, func(s string) bool { return len(s) > 2 })
	if !slices.Equal(strs, []string{"alpha", "charlie"}) {
		t.Errorf("Filter по строкам = %v", strs)
	}

	type point struct{ X, Y int }
	pts := Filter([]point{{1, 1}, {0, 5}, {2, 2}}, func(p point) bool { return p.X == p.Y })
	if !slices.Equal(pts, []point{{1, 1}, {2, 2}}) {
		t.Errorf("Filter по структурам = %v", pts)
	}
}

func TestFilterDoesNotShareMemory(t *testing.T) {
	in := []int{2, 4, 6}
	got := Filter(in, even)
	if len(got) != 3 {
		t.Fatalf("Filter(%v) = %v", in, got)
	}

	got[0] = 100
	if in[0] == 100 {
		t.Error("результат делит память со входом; нужен новый слайс")
	}
}

func TestFilterCallsPredicateOncePerElement(t *testing.T) {
	var seen []int
	Filter([]int{3, 1, 2}, func(x int) bool {
		seen = append(seen, x)
		return true
	})
	if !slices.Equal(seen, []int{3, 1, 2}) {
		t.Errorf("предикат вызван для %v, ожидалось по разу в порядке слайса: [3 1 2]", seen)
	}
}

func TestPartition(t *testing.T) {
	tests := []struct {
		name    string
		in      []int
		wantYes []int
		wantNo  []int
	}{
		{name: "nil", in: nil},
		{name: "всё подошло", in: []int{2, 4}, wantYes: []int{2, 4}},
		{name: "ничего не подошло", in: []int{1, 3}, wantNo: []int{1, 3}},
		{name: "смешанный", in: []int{1, 2, 3, 4}, wantYes: []int{2, 4}, wantNo: []int{1, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yes, no := Partition(tt.in, even)
			if !slices.Equal(yes, tt.wantYes) {
				t.Errorf("yes = %v, ожидалось %v", yes, tt.wantYes)
			}
			if !slices.Equal(no, tt.wantNo) {
				t.Errorf("no = %v, ожидалось %v", no, tt.wantNo)
			}
			if tt.wantYes == nil && yes != nil {
				t.Errorf("yes = %#v, ожидался nil", yes)
			}
			if tt.wantNo == nil && no != nil {
				t.Errorf("no = %#v, ожидался nil", no)
			}
		})
	}
}

func TestCount(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want int
	}{
		{name: "nil", in: nil, want: 0},
		{name: "ничего не подошло", in: []string{"a", "c"}, want: 0},
		{name: "часть подошла", in: []string{"a", "ab", "abc"}, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Count(tt.in, func(s string) bool { return strings.Contains(s, "b") })
			if got != tt.want {
				t.Errorf("Count(%v) = %d, ожидалось %d", tt.in, got, tt.want)
			}
		})
	}
}
