package solution

import "context"

// Drain суммирует значения из ch, пока канал не закроют или ctx не отменят.
func Drain(ctx context.Context, ch <-chan int) (int, error) {
	total := 0
	for {
		// select из двух готовых каналов выбирает случайно, поэтому
		// отмену проверяем в начале каждой итерации явно.
		if err := ctx.Err(); err != nil {
			return total, err
		}

		select {
		case v, ok := <-ch:
			if !ok {
				return total, nil
			}
			total += v
		case <-ctx.Done():
			return total, ctx.Err()
		}
	}
}
