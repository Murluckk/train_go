package solution

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, body)
	})
}

func call(t *testing.T, h http.Handler) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Result()
}

func TestChainOrder(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, "->"+name)
				next.ServeHTTP(w, r)
				order = append(order, name+"->")
			})
		}
	}

	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), mark("a"), mark("b"))

	call(t, h)

	want := "->a ->b handler b-> a->"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("порядок = %q, ожидался %q (первый в списке - внешний)", got, want)
	}
}

func TestChainWithoutMiddleware(t *testing.T) {
	h := ok("тело")
	if got := Chain(h); got == nil {
		t.Fatal("Chain(h) вернул nil")
	}
	resp := call(t, Chain(h))
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "тело" {
		t.Errorf("тело = %q, ожидалось %q", body, "тело")
	}
}

func TestWithHeader(t *testing.T) {
	h := Chain(ok("x"), WithHeader("X-Trainer", "drill"), WithHeader("X-Other", "1"))
	resp := call(t, h)

	if got := resp.Header.Get("X-Trainer"); got != "drill" {
		t.Errorf("X-Trainer = %q, ожидалось drill", got)
	}
	if got := resp.Header.Get("X-Other"); got != "1" {
		t.Errorf("X-Other = %q, ожидалось 1", got)
	}
}

func TestWithHeaderSetBeforeWrite(t *testing.T) {
	// Обработчик пишет тело сразу; заголовок обязан быть выставлен до этого.
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "поздно")
	}), WithHeader("X-Trainer", "drill"))

	resp := call(t, h)
	if got := resp.Header.Get("X-Trainer"); got != "drill" {
		t.Errorf("X-Trainer = %q: заголовок нужно ставить до вызова следующего обработчика", got)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("статус = %d, ожидался 418", resp.StatusCode)
	}
}

func TestRecoverer(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.Handler
		wantStatus int
	}{
		{
			name:       "паника до записи",
			handler:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("бум") }),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "паника после записи статуса",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				panic("поздно")
			}),
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "без паники ничего не меняется",
			handler:    ok("норм"),
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Chain(tt.handler, Recoverer())

			var resp *http.Response
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("паника вышла наружу: %v", p)
					}
				}()
				resp = call(t, h)
			}()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("статус = %d, ожидался %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.Handler
		wantStatus int
		wantWrote  int64
	}{
		{name: "обычный ответ", handler: ok("12345"), wantStatus: 200, wantWrote: 5},
		{
			name: "явный статус",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, "нет")
			}),
			wantStatus: 404, wantWrote: int64(len("нет")),
		},
		{
			name:       "обработчик ничего не сделал",
			handler:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			wantStatus: 200, wantWrote: 0,
		},
		{
			name: "повторный WriteHeader игнорируется",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.WriteHeader(http.StatusTeapot)
			}),
			wantStatus: 201, wantWrote: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				calls  int
				status int
				wrote  int64
			)
			h := Chain(tt.handler, Observe(func(s int, n int64) {
				calls++
				status, wrote = s, n
			}))
			call(t, h)

			if calls != 1 {
				t.Fatalf("record вызвана %d раз, ожидался ровно 1", calls)
			}
			if status != tt.wantStatus {
				t.Errorf("статус = %d, ожидался %d", status, tt.wantStatus)
			}
			if wrote != tt.wantWrote {
				t.Errorf("записано %d байт, ожидалось %d", wrote, tt.wantWrote)
			}
		})
	}
}

func TestObserveSeesTheRecoveredStatus(t *testing.T) {
	var status int
	h := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("бум") }),
		Observe(func(s int, _ int64) { status = s }),
		Recoverer(),
	)
	call(t, h)

	if status != http.StatusInternalServerError {
		t.Errorf("Observe увидел статус %d, ожидался 500: внешний middleware должен видеть итоговый ответ", status)
	}
}
