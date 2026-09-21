package solution

import "context"

// Drain суммирует значения из ch, пока канал не закроют или ctx не отменят.
func Drain(ctx context.Context, ch <-chan int) (int, error) {
	panic("не реализовано")
}
