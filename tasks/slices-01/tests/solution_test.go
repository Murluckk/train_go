package solution

import (
	"slices"
	"testing"
)

func TestDedup(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "nil остаётся nil", in: nil, want: nil},
		{name: "пустой слайс даёт nil", in: []string{}, want: nil},
		{name: "без повторов порядок сохраняется", in: []string{"c", "a", "b"}, want: []string{"c", "a", "b"}},
		{name: "подряд идущие повторы", in: []string{"a", "a", "a"}, want: []string{"a"}},
		{name: "повторы вразбивку", in: []string{"a", "b", "a", "c", "b"}, want: []string{"a", "b", "c"}},
		{name: "пустая строка - обычное значение", in: []string{"", "a", ""}, want: []string{"", "a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Dedup(tt.in)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Dedup(%#v) = %#v, ожидалось %#v", tt.in, got, tt.want)
			}
			if tt.want == nil && got != nil {
				t.Errorf("Dedup(%#v) вернул %#v (len=%d), а должен вернуть именно nil", tt.in, got, len(got))
			}
		})
	}
}

func TestDedupDoesNotMutateInput(t *testing.T) {
	in := []string{"b", "a", "b", "c"}
	before := slices.Clone(in)

	Dedup(in)

	if !slices.Equal(in, before) {
		t.Errorf("входной слайс изменился: было %#v, стало %#v", before, in)
	}
}

func TestDedupReturnsFreshSlice(t *testing.T) {
	in := []string{"a", "b", "c"}
	got := Dedup(in)
	if len(got) != len(in) {
		t.Fatalf("Dedup(%#v) = %#v", in, got)
	}

	got[0] = "изменено"
	if in[0] == "изменено" {
		t.Error("результат делит память с входным слайсом; нужен именно новый слайс")
	}
}

func BenchmarkDedup(b *testing.B) {
	in := make([]string, 0, 1000)
	for i := range 1000 {
		in = append(in, string(rune('a'+i%26)))
	}
	b.ResetTimer()
	for b.Loop() {
		Dedup(in)
	}
}
