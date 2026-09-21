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
	if timeout <= 0 {
		// Неблокирующая попытка: default срабатывает, если готового
		// значения нет.
		select {
		case v, ok := <-ch:
			if !ok {
				return 0, ErrClosed
			}
			return v, nil
		default:
			return 0, ErrTimeout
		}
	}

	// Таймер один, и он останавливается: иначе он держал бы себя в куче
	// до срабатывания.
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case v, ok := <-ch:
		if !ok {
			// Закрытый канал всегда готов к чтению, так что без проверки
			// ok эта ветка выдавала бы ноль вместо ошибки.
			return 0, ErrClosed
		}
		return v, nil
	case <-timer.C:
		return 0, ErrTimeout
	}
}
