package solution

import (
	"errors"
	"time"
)

// ErrTimeout означает, что значение не пришло за отведённое время.
var ErrTimeout = errors.New("истекло время ожидания")

// ErrClosed означает, что канал закрыли, так и не прислав значение.
var ErrClosed = errors.New("канал закрыт")

// WaitFor ждёт значение из ch не дольше timeout.
func WaitFor(ch <-chan int, timeout time.Duration) (int, error) {
	panic("не реализовано")
}
