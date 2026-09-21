package solution

import (
	"bytes"
	"io"
)

// CountingWriter оборачивает io.Writer и считает прошедшие через него
// байты и строки.
type CountingWriter struct {
	w     io.Writer
	bytes int64
	lines int64
}

// NewCountingWriter создаёт счётчик поверх w.
func NewCountingWriter(w io.Writer) *CountingWriter {
	return &CountingWriter{w: w}
}

// Write записывает p в нижележащий writer и обновляет счётчики.
func (c *CountingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	// Считаем только то, что действительно ушло: при короткой записи
	// хвост p до нижележащего writer не дошёл.
	if n > 0 {
		c.bytes += int64(n)
		c.lines += int64(bytes.Count(p[:n], []byte{'\n'}))
	}
	return n, err
}

// Bytes возвращает число фактически записанных байт.
func (c *CountingWriter) Bytes() int64 { return c.bytes }

// Lines возвращает число записанных переводов строки.
func (c *CountingWriter) Lines() int64 { return c.lines }
