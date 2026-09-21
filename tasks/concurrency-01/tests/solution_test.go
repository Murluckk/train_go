package solution

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelMap(t *testing.T) {
	double := func(x int) int { return x * 2 }

	tests := []struct {
		name string
		in   []int
		f    func(int) int
		want []int
	}{
		{name: "nil остаётся nil", in: nil, f: double, want: nil},
		{name: "пустой слайс", in: []int{}, f: double, want: []int{}},
		{name: "один элемент", in: []int{21}, f: double, want: []int{42}},
		{name: "порядок сохраняется", in: []int{1, 2, 3, 4, 5}, f: double, want: []int{2, 4, 6, 8, 10}},
		{name: "функция может быть не чистой по времени", in: []int{3, 2, 1}, f: func(x int) int {
			// Обратный порядок по длительности: если результаты пишутся
			// по мере готовности, порядок развалится.
			time.Sleep(time.Duration(x) * 10 * time.Millisecond)
			return x
		}, want: []int{3, 2, 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParallelMap(tt.in, tt.f)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParallelMap(%v) = %v, ожидалось %v", tt.in, got, tt.want)
			}
			if tt.want == nil && got != nil {
				t.Errorf("ParallelMap(nil) = %#v, ожидался именно nil", got)
			}
		})
	}
}

func TestParallelMapCallsFOncePerElement(t *testing.T) {
	var calls atomic.Int64
	in := make([]int, 100)
	for i := range in {
		in[i] = i
	}

	got := ParallelMap(in, func(x int) int {
		calls.Add(1)
		return x + 1
	})

	if n := calls.Load(); n != int64(len(in)) {
		t.Errorf("f вызвана %d раз, ожидалось %d", n, len(in))
	}
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("got[%d] = %d, ожидалось %d", i, v, i+1)
		}
	}
}

func TestParallelMapIsActuallyParallel(t *testing.T) {
	// Все горутины должны дойти до барьера одновременно. Если функция
	// выполняется последовательно, барьер не соберётся и тест зависнет
	// до таймаута go test.
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)

	in := make([]int, n)
	done := make(chan []int, 1)
	go func() {
		done <- ParallelMap(in, func(x int) int {
			wg.Done()
			wg.Wait()
			return x
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ParallelMap не запустил элементы параллельно: горутины не встретились на барьере")
	}
}

func TestParallelMapNoRace(t *testing.T) {
	// Полезен только под -race, но безвреден и без него.
	in := make([]int, 500)
	for i := range in {
		in[i] = i
	}
	got := ParallelMap(in, func(x int) int { return x * x })
	for i := range in {
		if got[i] != i*i {
			t.Fatalf("got[%d] = %d, ожидалось %d", i, got[i], i*i)
		}
	}
}
