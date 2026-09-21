package solution

import (
	"maps"
	"slices"
	"testing"
)

func TestInvert(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]int
		want map[int][]string
	}{
		{name: "nil даёт пустую мапу", in: nil, want: map[int][]string{}},
		{name: "пустая мапа", in: map[string]int{}, want: map[int][]string{}},
		{
			name: "один элемент",
			in:   map[string]int{"a": 1},
			want: map[int][]string{1: {"a"}},
		},
		{
			name: "уникальные значения",
			in:   map[string]int{"a": 1, "b": 2},
			want: map[int][]string{1: {"a"}, 2: {"b"}},
		},
		{
			name: "коллизия значений собирается в слайс",
			in:   map[string]int{"a": 1, "b": 2, "c": 1},
			want: map[int][]string{1: {"a", "c"}, 2: {"b"}},
		},
		{
			name: "отрицательные и нулевые значения",
			in:   map[string]int{"x": 0, "y": -1, "z": 0},
			want: map[int][]string{0: {"x", "z"}, -1: {"y"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Invert(tt.in)
			if got == nil {
				t.Fatalf("Invert(%v) вернул nil, а должен вернуть непустую ссылку на мапу", tt.in)
			}
			if !maps.EqualFunc(got, tt.want, slices.Equal) {
				t.Errorf("Invert(%v) = %v, ожидалось %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestInvertSortsKeys(t *testing.T) {
	// Мапа обходится в случайном порядке, поэтому один прогон ничего не
	// доказывает: повторяем, пока случайность не проявится.
	in := map[string]int{"delta": 1, "alpha": 1, "charlie": 1, "bravo": 1}
	want := []string{"alpha", "bravo", "charlie", "delta"}

	for i := range 50 {
		got := Invert(in)[1]
		if !slices.Equal(got, want) {
			t.Fatalf("прогон %d: Invert(...)[1] = %v, ожидалось %v (ключи должны быть отсортированы)", i, got, want)
		}
	}
}

func TestInvertDoesNotMutateInput(t *testing.T) {
	in := map[string]int{"a": 1, "b": 1}
	before := maps.Clone(in)

	Invert(in)

	if !maps.Equal(in, before) {
		t.Errorf("входная мапа изменилась: было %v, стало %v", before, in)
	}
}
