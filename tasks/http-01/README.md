# HTTP middleware и цепочка

```go
type Middleware func(http.Handler) http.Handler

func Chain(h http.Handler, mws ...Middleware) http.Handler
func WithHeader(key, value string) Middleware
func Recoverer() Middleware
func Observe(record func(status int, wrote int64)) Middleware
```

## Требования

- `Chain` применяет middleware так, чтобы **первый в списке был внешним**:
  `Chain(h, a, b)` выполняется как `a(b(h))`. Это порядок, к которому
  все привыкли по chi и gorilla.
- `Chain(h)` без middleware возвращает `h` как есть.
- `WithHeader` ставит заголовок **до** вызова следующего обработчика:
  после `WriteHeader` заголовки уже ушли.
- `Recoverer` ловит панику из обработчика и отвечает `500`. Если
  обработчик уже успел записать статус, второй `WriteHeader` делать
  нельзя — `net/http` на это ругается в лог.
- `Observe` вызывает `record` ровно один раз после обработчика, передавая
  фактический статус и число записанных байт. Обработчик, который ничего
  не вызвал, всё равно даёт статус `200` — так работает `net/http`.

## Где обычно ошибаются

Чтобы узнать статус, `ResponseWriter` надо обернуть. Обёртка обязана
запомнить первый `WriteHeader` и проигнорировать повторные, а `Write`
без явного `WriteHeader` должен зафиксировать `200`.
