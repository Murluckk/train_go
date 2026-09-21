package solution

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestWaitFor(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() <-chan int
		timeout time.Duration
		want    int
		wantErr error
	}{
		{
			name: "значение уже в буфере",
			setup: func() <-chan int {
				ch := make(chan int, 1)
				ch <- 42
				return ch
			},
			timeout: time.Second,
			want:    42,
		},
		{
			name: "значение приходит позже",
			setup: func() <-chan int {
				ch := make(chan int)
				go func() {
					time.Sleep(20 * time.Millisecond)
					ch <- 7
				}()
				return ch
			},
			timeout: 2 * time.Second,
			want:    7,
		},
		{
			name:    "никто не пишет - таймаут",
			setup:   func() <-chan int { return make(chan int) },
			timeout: 30 * time.Millisecond,
			wantErr: ErrTimeout,
		},
		{
			name:    "nil-канал - таймаут",
			setup:   func() <-chan int { return nil },
			timeout: 30 * time.Millisecond,
			wantErr: ErrTimeout,
		},
		{
			name: "закрытый канал",
			setup: func() <-chan int {
				ch := make(chan int)
				close(ch)
				return ch
			},
			timeout: time.Second,
			wantErr: ErrClosed,
		},
		{
			name: "канал закрывают во время ожидания",
			setup: func() <-chan int {
				ch := make(chan int)
				go func() {
					time.Sleep(20 * time.Millisecond)
					close(ch)
				}()
				return ch
			},
			timeout: 2 * time.Second,
			wantErr: ErrClosed,
		},
		{
			name: "нулевой таймаут с готовым значением",
			setup: func() <-chan int {
				ch := make(chan int, 1)
				ch <- 5
				return ch
			},
			timeout: 0,
			want:    5,
		},
		{
			name:    "нулевой таймаут без значения",
			setup:   func() <-chan int { return make(chan int) },
			timeout: 0,
			wantErr: ErrTimeout,
		},
		{
			name:    "отрицательный таймаут",
			setup:   func() <-chan int { return make(chan int) },
			timeout: -time.Second,
			wantErr: ErrTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WaitFor(tt.setup(), tt.timeout)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("WaitFor() вернул ошибку %v, ожидалась %v", err, tt.wantErr)
				}
				if got != 0 {
					t.Errorf("WaitFor() = %d при ошибке, ожидался 0", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("WaitFor() вернул ошибку %v", err)
			}
			if got != tt.want {
				t.Errorf("WaitFor() = %d, ожидалось %d", got, tt.want)
			}
		})
	}
}

func TestWaitForRespectsTheDeadline(t *testing.T) {
	started := time.Now()
	if _, err := WaitFor(make(chan int), 50*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("ожидался ErrTimeout, получено %v", err)
	}
	elapsed := time.Since(started)

	if elapsed < 40*time.Millisecond {
		t.Errorf("вернулось через %s, а таймаут был 50ms: похоже, ожидания не было", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("вернулось через %s, ожидалось около 50ms", elapsed)
	}
}

func TestWaitForDoesNotLeakGoroutines(t *testing.T) {
	// Прогреваем рантайм, чтобы не считать служебные горутины.
	if _, err := WaitFor(make(chan int), 10*time.Millisecond); err == nil {
		t.Fatal("ожидался таймаут")
	}
	runtime.GC()
	before := runtime.NumGoroutine()

	for range 200 {
		if _, err := WaitFor(make(chan int), time.Millisecond); !errors.Is(err, ErrTimeout) {
			t.Fatalf("ожидался ErrTimeout, получено %v", err)
		}
	}

	// Даём рантайму дошедулить то, что уже завершается.
	for range 20 {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("после 200 вызовов горутин стало %d против %d: WaitFor оставляет их висеть",
		runtime.NumGoroutine(), before)
}
