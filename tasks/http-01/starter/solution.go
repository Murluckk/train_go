package solution

import "net/http"

// Middleware оборачивает обработчик.
type Middleware func(http.Handler) http.Handler

// Chain применяет middleware к h: первый в списке становится внешним.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	panic("не реализовано")
}

// WithHeader ставит заголовок перед вызовом следующего обработчика.
func WithHeader(key, value string) Middleware {
	panic("не реализовано")
}

// Recoverer превращает панику обработчика в ответ 500.
func Recoverer() Middleware {
	panic("не реализовано")
}

// Observe вызывает record после обработчика с фактическим статусом
// и числом записанных байт.
func Observe(record func(status int, wrote int64)) Middleware {
	panic("не реализовано")
}
