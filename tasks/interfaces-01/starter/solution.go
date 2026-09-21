package solution

import "io"

// CountingWriter оборачивает io.Writer и считает прошедшие через него
// байты и строки.
type CountingWriter struct {
	// TODO
}

// NewCountingWriter создаёт счётчик поверх w.
func NewCountingWriter(w io.Writer) *CountingWriter {
	panic("не реализовано")
}

// Write записывает p в нижележащий writer и обновляет счётчики.
func (c *CountingWriter) Write(p []byte) (int, error) {
	panic("не реализовано")
}

// Bytes возвращает число фактически записанных байт.
func (c *CountingWriter) Bytes() int64 {
	panic("не реализовано")
}

// Lines возвращает число записанных переводов строки.
func (c *CountingWriter) Lines() int64 {
	panic("не реализовано")
}
