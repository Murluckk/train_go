package solution

// Filter возвращает элементы xs, для которых keep вернула true.
func Filter[T any](xs []T, keep func(T) bool) []T {
	panic("не реализовано")
}

// Partition делит xs на подошедшие и не подошедшие за один проход.
func Partition[T any](xs []T, keep func(T) bool) (yes, no []T) {
	panic("не реализовано")
}

// Count считает элементы, для которых keep вернула true.
func Count[T any](xs []T, keep func(T) bool) int {
	panic("не реализовано")
}
