package solution

import "errors"

// ErrMissing означает, что обязательное поле не задано.
var ErrMissing = errors.New("обязательное поле отсутствует")

// ErrRange означает, что значение вне допустимого диапазона.
var ErrRange = errors.New("значение вне допустимого диапазона")

// FieldError указывает, в каком именно поле проблема.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string {
	panic("не реализовано")
}

func (e *FieldError) Unwrap() error {
	panic("не реализовано")
}

// Config - разобранная конфигурация.
type Config struct {
	Host string
	Port int
}

// ParseConfig разбирает raw в Config.
func ParseConfig(raw map[string]string) (Config, error) {
	panic("не реализовано")
}
