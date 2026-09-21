package solution

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration - это time.Duration, умеющий читаться из JSON как строка
// ("1m30s") или как число секунд (90).
type Duration time.Duration

// UnmarshalJSON разбирает строку или число секунд.
func (d *Duration) UnmarshalJSON(data []byte) error {
	// any вместо ручного разбора: json.Unmarshal уже умеет и кавычки,
	// и экранирование, и числа с плавающей точкой.
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("duration: %w", err)
	}

	switch v := raw.(type) {
	case nil:
		// Как у стандартных типов: null оставляет значение как было.
		return nil
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("duration %q: %w", v, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		*d = Duration(v * float64(time.Second))
		return nil
	default:
		return fmt.Errorf("duration: ожидалась строка или число, получено %s", data)
	}
}

// MarshalJSON пишет длительность строкой.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// String возвращает человекочитаемую форму.
func (d Duration) String() string { return time.Duration(d).String() }
