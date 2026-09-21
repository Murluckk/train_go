package solution

// Filter возвращает элементы xs, для которых keep вернула true.
func Filter[T any](xs []T, keep func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

// Partition делит xs на подошедшие и не подошедшие за один проход.
func Partition[T any](xs []T, keep func(T) bool) (yes, no []T) {
	for _, x := range xs {
		if keep(x) {
			yes = append(yes, x)
		} else {
			no = append(no, x)
		}
	}
	return yes, no
}

// Count считает элементы, для которых keep вернула true.
func Count[T any](xs []T, keep func(T) bool) int {
	n := 0
	for _, x := range xs {
		if keep(x) {
			n++
		}
	}
	return n
}
