package solution

import "slices"

// Invert меняет местами ключи и значения. Ключи с одинаковым значением
// собираются в отсортированный слайс.
func Invert(m map[string]int) map[int][]string {
	out := make(map[int][]string, len(m))
	for k, v := range m {
		out[v] = append(out[v], k)
	}
	// Порядок обхода мапы не определён, поэтому без сортировки результат
	// был бы недетерминированным.
	for _, keys := range out {
		slices.Sort(keys)
	}
	return out
}
