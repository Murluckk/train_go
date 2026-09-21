package solution

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// Проверка на этапе компиляции: обёртка обязана быть io.Writer.
var _ io.Writer = (*CountingWriter)(nil)

// shortWriter записывает не больше limit байт за раз и возвращает ошибку,
// как того требует контракт io.Writer.
type shortWriter struct {
	limit int
	buf   bytes.Buffer
	err   error
}

func (s *shortWriter) Write(p []byte) (int, error) {
	if len(p) <= s.limit {
		n, _ := s.buf.Write(p)
		return n, nil
	}
	n, _ := s.buf.Write(p[:s.limit])
	return n, s.err
}

func TestCountingWriter(t *testing.T) {
	tests := []struct {
		name      string
		writes    []string
		wantBytes int64
		wantLines int64
		wantOut   string
	}{
		{name: "ничего не записано", wantBytes: 0, wantLines: 0},
		{name: "одна строка без перевода", writes: []string{"hello"}, wantBytes: 5, wantLines: 0, wantOut: "hello"},
		{name: "одна строка с переводом", writes: []string{"hello\n"}, wantBytes: 6, wantLines: 1, wantOut: "hello\n"},
		{
			name:      "несколько записей",
			writes:    []string{"a\nb", "\nc\n"},
			wantBytes: 6, wantLines: 3, wantOut: "a\nb\nc\n",
		},
		{name: "пустая запись", writes: []string{""}, wantBytes: 0, wantLines: 0},
		{name: "только переводы строк", writes: []string{"\n\n\n"}, wantBytes: 3, wantLines: 3, wantOut: "\n\n\n"},
		{
			name:      "многобайтные руны считаются в байтах",
			writes:    []string{"привет\n"},
			wantBytes: int64(len("привет\n")), wantLines: 1, wantOut: "привет\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sink bytes.Buffer
			c := NewCountingWriter(&sink)

			for _, s := range tt.writes {
				n, err := c.Write([]byte(s))
				if err != nil {
					t.Fatalf("Write(%q) вернул ошибку %v", s, err)
				}
				if n != len(s) {
					t.Fatalf("Write(%q) = %d, ожидалось %d", s, n, len(s))
				}
			}

			if got := c.Bytes(); got != tt.wantBytes {
				t.Errorf("Bytes() = %d, ожидалось %d", got, tt.wantBytes)
			}
			if got := c.Lines(); got != tt.wantLines {
				t.Errorf("Lines() = %d, ожидалось %d", got, tt.wantLines)
			}
			if got := sink.String(); got != tt.wantOut {
				t.Errorf("в нижележащий writer ушло %q, ожидалось %q", got, tt.wantOut)
			}
		})
	}
}

func TestCountingWriterPropagatesShortWrite(t *testing.T) {
	boom := errors.New("диск кончился")
	sw := &shortWriter{limit: 3, err: boom}
	c := NewCountingWriter(sw)

	n, err := c.Write([]byte("a\nb\nc\n"))

	if !errors.Is(err, boom) {
		t.Errorf("Write() вернул ошибку %v, ожидалась %v: обёртка не должна глотать ошибку", err, boom)
	}
	if n != 3 {
		t.Errorf("Write() = %d, ожидалось 3: нужно вернуть ровно то, что вернул нижележащий writer", n)
	}
	if got := c.Bytes(); got != 3 {
		t.Errorf("Bytes() = %d, ожидалось 3: считаем только фактически записанное", got)
	}
	if got := c.Lines(); got != 1 {
		t.Errorf("Lines() = %d, ожидалось 1: перевод строки во второй половине p записан не был", got)
	}
}

func TestCountingWriterWorksWithFprintf(t *testing.T) {
	var sink strings.Builder
	c := NewCountingWriter(&sink)

	if _, err := io.Copy(c, strings.NewReader("one\ntwo\nthree")); err != nil {
		t.Fatalf("io.Copy вернул ошибку %v", err)
	}
	if got, want := c.Bytes(), int64(len("one\ntwo\nthree")); got != want {
		t.Errorf("Bytes() = %d, ожидалось %d", got, want)
	}
	if got := c.Lines(); got != 2 {
		t.Errorf("Lines() = %d, ожидалось 2: последняя строка без перевода не считается", got)
	}
}
