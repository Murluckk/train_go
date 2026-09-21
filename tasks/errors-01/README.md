# Свой тип ошибки и wrapping

Нужно распарсить настройки и вернуть ошибку так, чтобы вызывающий код мог
разобраться в ней программно, а не сравнением строк.

```go
var ErrMissing = errors.New("обязательное поле отсутствует")

type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string
func (e *FieldError) Unwrap() error

type Config struct {
	Host string
	Port int
}

func ParseConfig(raw map[string]string) (Config, error)
```

## Требования

- `ParseConfig` читает ключи `host` и `port`. Оба обязательны.
- Если ключа нет или он пустой — вернуть ошибку, для которой
  `errors.Is(err, ErrMissing)` истинно, а `errors.As` достаёт
  `*FieldError` с нужным `Field`.
- Если `port` не парсится в число — обернуть ошибку от `strconv.Atoi`
  так, чтобы `errors.Is(err, strconv.ErrSyntax)` осталось истинным.
- Если `port` вне диапазона 1..65535 — вернуть `*FieldError` с полем
  `port` и ошибкой `ErrRange` (объяви её сам через `errors.New`).
- `Error()` должен включать имя поля и текст вложенной ошибки:
  `port: обязательное поле отсутствует`.
- При ошибке возвращается нулевой `Config`.

## Смысл упражнения

`%w` вместо `%v`, `Unwrap` на своём типе и цепочка, которая переживает
двойное оборачивание. Проверь себя: `errors.Is` идёт по цепочке
`Unwrap`, а `errors.As` — тоже, но с приведением типа.
