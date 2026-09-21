package solution

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainReadsUntilClose(t *testing.T) {
	tests := []struct {
		name string
		vals []int
		want int
	}{
		{name: "пустой канал закрыт сразу", vals: nil, want: 0},
		{name: "одно значение", vals: []int{5}, want: 5},
		{name: "несколько значений", vals: []int{1, 2, 3, 4}, want: 10},
		{name: "отрицательные значения", vals: []int{5, -3, -2}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan int, len(tt.vals))
			for _, v := range tt.vals {
				ch <- v
			}
			close(ch)

			got, err := Drain(t.Context(), ch)
			if err != nil {
				t.Fatalf("Drain() вернул ошибку %v", err)
			}
			if got != tt.want {
				t.Errorf("Drain() = %d, ожидалось %d", got, tt.want)
			}
		})
	}
}

func TestDrainStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ch := make(chan int)

	go func() {
		ch <- 1
		ch <- 2
		cancel()
		// Канал не закрываем: выйти можно только по отмене.
	}()

	got, err := Drain(ctx, ch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain() вернул ошибку %v, ожидалась context.Canceled", err)
	}
	if got != 3 {
		t.Errorf("Drain() = %d, ожидалось 3: сумма, накопленная до отмены", got)
	}
}

func TestDrainStopsOnDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := Drain(ctx, make(chan int)); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Drain() вернул ошибку %v, ожидалась context.DeadlineExceeded", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain() не вернулась после истечения дедлайна")
	}
}

func TestDrainWithAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	ch <- 3

	got, err := Drain(ctx, ch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain() вернул ошибку %v, ожидалась context.Canceled", err)
	}
	if got != 0 {
		t.Errorf("Drain() = %d, ожидался 0: из уже отменённого контекста читать нельзя", got)
	}
	if len(ch) != 3 {
		t.Errorf("в канале осталось %d значений из 3: значения не должны вычитываться после отмены", len(ch))
	}
}

// Отмена и готовые данные приходят одновременно: select выберет случайную
// ветку, поэтому сценарий повторяется много раз.
func TestDrainDoesNotReadAfterCancel(t *testing.T) {
	for i := range 300 {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan int, 10)
		for range 10 {
			ch <- 1
		}
		cancel()

		got, err := Drain(ctx, ch)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("прогон %d: ошибка %v, ожидалась context.Canceled", i, err)
		}
		if got != 0 {
			t.Fatalf("прогон %d: Drain() = %d, ожидался 0 при уже отменённом контексте", i, got)
		}
	}
}

func TestDrainReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)
		Drain(ctx, make(chan int))
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain() зависла после отмены контекста")
	}
}
