package solution

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestParseConfigOK(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]string
		want Config
	}{
		{
			name: "минимальный набор",
			raw:  map[string]string{"host": "localhost", "port": "8080"},
			want: Config{Host: "localhost", Port: 8080},
		},
		{
			name: "граничный порт 1",
			raw:  map[string]string{"host": "h", "port": "1"},
			want: Config{Host: "h", Port: 1},
		},
		{
			name: "граничный порт 65535",
			raw:  map[string]string{"host": "h", "port": "65535"},
			want: Config{Host: "h", Port: 65535},
		},
		{
			name: "лишние ключи игнорируются",
			raw:  map[string]string{"host": "h", "port": "80", "debug": "true"},
			want: Config{Host: "h", Port: 80},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConfig(tt.raw)
			if err != nil {
				t.Fatalf("ParseConfig(%v) вернул ошибку %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseConfig(%v) = %+v, ожидалось %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseConfigErrors(t *testing.T) {
	tests := []struct {
		name      string
		raw       map[string]string
		wantField string
		wantIs    error
	}{
		{name: "нет host", raw: map[string]string{"port": "80"}, wantField: "host", wantIs: ErrMissing},
		{name: "пустой host", raw: map[string]string{"host": "", "port": "80"}, wantField: "host", wantIs: ErrMissing},
		{name: "нет port", raw: map[string]string{"host": "h"}, wantField: "port", wantIs: ErrMissing},
		{name: "пустой port", raw: map[string]string{"host": "h", "port": ""}, wantField: "port", wantIs: ErrMissing},
		{name: "port не число", raw: map[string]string{"host": "h", "port": "восемьдесят"}, wantField: "port", wantIs: strconv.ErrSyntax},
		{name: "port ноль", raw: map[string]string{"host": "h", "port": "0"}, wantField: "port", wantIs: ErrRange},
		{name: "port слишком большой", raw: map[string]string{"host": "h", "port": "70000"}, wantField: "port", wantIs: ErrRange},
		{name: "port отрицательный", raw: map[string]string{"host": "h", "port": "-1"}, wantField: "port", wantIs: ErrRange},
		{name: "пустая мапа", raw: map[string]string{}, wantField: "host", wantIs: ErrMissing},
		{name: "nil мапа", raw: nil, wantField: "host", wantIs: ErrMissing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConfig(tt.raw)
			if err == nil {
				t.Fatalf("ParseConfig(%v) = %+v, ожидалась ошибка", tt.raw, got)
			}
			if got != (Config{}) {
				t.Errorf("при ошибке вернулся %+v, ожидался нулевой Config", got)
			}

			if !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false для err = %v; проверь, что оборачиваешь через %%w и есть Unwrap", tt.wantIs, err)
			}

			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("errors.As(err, **FieldError) = false для err = %v", err)
			}
			if fe.Field != tt.wantField {
				t.Errorf("FieldError.Field = %q, ожидалось %q", fe.Field, tt.wantField)
			}
		})
	}
}

func TestFieldErrorMessage(t *testing.T) {
	err := &FieldError{Field: "port", Err: ErrMissing}

	msg := err.Error()
	if !strings.Contains(msg, "port") {
		t.Errorf("Error() = %q, в тексте должно быть имя поля", msg)
	}
	if !strings.Contains(msg, ErrMissing.Error()) {
		t.Errorf("Error() = %q, в тексте должна быть вложенная ошибка", msg)
	}
}

func TestFieldErrorUnwrap(t *testing.T) {
	inner := errors.New("внутренняя")
	err := &FieldError{Field: "x", Err: inner}

	if got := errors.Unwrap(err); got != inner {
		t.Errorf("errors.Unwrap(err) = %v, ожидалось %v", got, inner)
	}
}

func TestErrorChainSurvivesDoubleWrapping(t *testing.T) {
	_, err := ParseConfig(map[string]string{"host": "h", "port": "abc"})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	// Ошибка обёрнута дважды: FieldError поверх fmt.Errorf поверх strconv.
	// Цепочка должна пережить оба уровня.
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Errorf("errors.Is(err, strconv.ErrSyntax) = false для %v", err)
	}
	var numErr *strconv.NumError
	if !errors.As(err, &numErr) {
		t.Errorf("errors.As(err, **strconv.NumError) = false для %v", err)
	}
}
