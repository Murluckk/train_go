package solution

import "sync"

// ParallelMap применяет f к каждому элементу xs в отдельной горутине
// и возвращает результаты в исходном порядке.
func ParallelMap(xs []int, f func(int) int) []int {
	if xs == nil {
		return nil
	}

	// Слайс выделен заранее, и каждая горутина пишет в свою ячейку:
	// разные индексы одного слайса - это не гонка, синхронизация не нужна.
	out := make([]int, len(xs))

	var wg sync.WaitGroup
	for i, x := range xs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = f(x)
		}()
	}
	wg.Wait()

	return out
}
