package solution

import "net/http"

// Middleware оборачивает обработчик.
type Middleware func(http.Handler) http.Handler

// Chain применяет middleware к h: первый в списке становится внешним.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	// Идём с конца: последний в списке оборачивает обработчик первым и
	// оказывается самым внутренним.
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// WithHeader ставит заголовок перед вызовом следующего обработчика.
func WithHeader(key, value string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// После WriteHeader заголовки уже отправлены, так что только до.
			w.Header().Set(key, value)
			next.ServeHTTP(w, r)
		})
	}
}

// Recoverer превращает панику обработчика в ответ 500.
func Recoverer() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				if p := recover(); p != nil {
					// Если статус уже ушёл, второй WriteHeader только
					// нагадит в лог: ответ всё равно не переписать.
					if !rec.wroteHeader {
						rec.WriteHeader(http.StatusInternalServerError)
					}
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// Observe вызывает record после обработчика с фактическим статусом
// и числом записанных байт.
func Observe(record func(status int, wrote int64)) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			defer func() { record(rec.status, rec.wrote) }()
			next.ServeHTTP(rec, r)
		})
	}
}

// recorder запоминает статус и объём ответа.
type recorder struct {
	http.ResponseWriter
	status      int
	wrote       int64
	wroteHeader bool
}

func (r *recorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	// Write без WriteHeader неявно означает 200.
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(p)
	r.wrote += int64(n)
	return n, err
}
