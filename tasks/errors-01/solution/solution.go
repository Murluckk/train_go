package solution

import (
	"errors"
	"fmt"
	"strconv"
)

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
	return e.Field + ": " + e.Err.Error()
}

// Unwrap открывает цепочку для errors.Is и errors.As.
func (e *FieldError) Unwrap() error { return e.Err }

// Config - разобранная конфигурация.
type Config struct {
	Host string
	Port int
}

// ParseConfig разбирает raw в Config.
func ParseConfig(raw map[string]string) (Config, error) {
	host := raw["host"]
	if host == "" {
		return Config{}, &FieldError{Field: "host", Err: ErrMissing}
	}

	rawPort := raw["port"]
	if rawPort == "" {
		return Config{}, &FieldError{Field: "port", Err: ErrMissing}
	}

	port, err := strconv.Atoi(rawPort)
	if err != nil {
		// %w сохраняет strconv.ErrSyntax в цепочке, %v бы её оборвал.
		return Config{}, &FieldError{Field: "port", Err: fmt.Errorf("разбор числа: %w", err)}
	}
	if port < 1 || port > 65535 {
		return Config{}, &FieldError{Field: "port", Err: ErrRange}
	}

	return Config{Host: host, Port: port}, nil
}
