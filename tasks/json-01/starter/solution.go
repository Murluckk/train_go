package solution

import "time"

// Duration - это time.Duration, умеющий читаться из JSON как строка
// ("1m30s") или как число секунд (90).
type Duration time.Duration

// UnmarshalJSON разбирает строку или число секунд.
func (d *Duration) UnmarshalJSON(data []byte) error {
	panic("не реализовано")
}

// MarshalJSON пишет длительность строкой.
func (d Duration) MarshalJSON() ([]byte, error) {
	panic("не реализовано")
}

// String возвращает человекочитаемую форму.
func (d Duration) String() string { return time.Duration(d).String() }
