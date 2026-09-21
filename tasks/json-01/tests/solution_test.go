package solution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{name: "строка с минутами и секундами", in: `"1m30s"`, want: 90 * time.Second},
		{name: "строка с миллисекундами", in: `"500ms"`, want: 500 * time.Millisecond},
		{name: "строка с часами", in: `"2h"`, want: 2 * time.Hour},
		{name: "нулевая строка", in: `"0s"`, want: 0},
		{name: "отрицательная строка", in: `"-5s"`, want: -5 * time.Second},
		{name: "целое число - секунды", in: `30`, want: 30 * time.Second},
		{name: "ноль", in: `0`, want: 0},
		{name: "дробное число", in: `1.5`, want: 1500 * time.Millisecond},
		{name: "отрицательное число", in: `-2`, want: -2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.in), &d); err != nil {
				t.Fatalf("Unmarshal(%s) вернул ошибку %v", tt.in, err)
			}
			if time.Duration(d) != tt.want {
				t.Errorf("Unmarshal(%s) = %s, ожидалось %s", tt.in, time.Duration(d), tt.want)
			}
		})
	}
}

func TestUnmarshalJSONNullLeavesValueAlone(t *testing.T) {
	d := Duration(42 * time.Second)
	if err := json.Unmarshal([]byte(`null`), &d); err != nil {
		t.Fatalf("Unmarshal(null) вернул ошибку %v", err)
	}
	if time.Duration(d) != 42*time.Second {
		t.Errorf("после null значение стало %s, ожидалось 42s", time.Duration(d))
	}
}

func TestUnmarshalJSONErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "мусор в строке", in: `"вчера"`},
		{name: "число без единицы в строке", in: `"30"`},
		{name: "объект", in: `{"seconds": 30}`},
		{name: "массив", in: `[30]`},
		{name: "булево", in: `true`},
		{name: "сломанный json", in: `"незакрытая`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.in), &d); err == nil {
				t.Errorf("Unmarshal(%s) прошёл без ошибки, получилось %s", tt.in, time.Duration(d))
			}
		})
	}
}

func TestUnmarshalErrorMentionsTheValue(t *testing.T) {
	var d Duration
	err := json.Unmarshal([]byte(`"позавчера"`), &d)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "позавчера") {
		t.Errorf("ошибка %q не упоминает исходное значение; в большом конфиге её не найти", err)
	}
}

func TestMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "полторы минуты", in: 90 * time.Second, want: `"1m30s"`},
		{name: "ноль", in: 0, want: `"0s"`},
		{name: "миллисекунды", in: 500 * time.Millisecond, want: `"500ms"`},
		{name: "отрицательная", in: -3 * time.Second, want: `"-3s"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(Duration(tt.in))
			if err != nil {
				t.Fatalf("Marshal(%s) вернул ошибку %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal(%s) = %s, ожидалось %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	for _, d := range []time.Duration{0, time.Nanosecond, time.Second, 90 * time.Second, 3*time.Hour + 25*time.Minute, -time.Minute} {
		encoded, err := json.Marshal(Duration(d))
		if err != nil {
			t.Fatalf("Marshal(%s): %v", d, err)
		}
		var back Duration
		if err := json.Unmarshal(encoded, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", encoded, err)
		}
		if time.Duration(back) != d {
			t.Errorf("round-trip %s -> %s -> %s", d, encoded, time.Duration(back))
		}
	}
}

// Ради этого всё и затевалось: тип должен нормально жить внутри структуры.
func TestInsideAStruct(t *testing.T) {
	type config struct {
		Name    string   `json:"name"`
		Timeout Duration `json:"timeout"`
		Retry   Duration `json:"retry"`
	}

	var got config
	input := `{"name": "api", "timeout": "1m30s", "retry": 5}`
	if err := json.Unmarshal([]byte(input), &got); err != nil {
		t.Fatalf("Unmarshal вернул ошибку %v", err)
	}
	if got.Name != "api" {
		t.Errorf("Name = %q", got.Name)
	}
	if time.Duration(got.Timeout) != 90*time.Second {
		t.Errorf("Timeout = %s, ожидалось 1m30s", time.Duration(got.Timeout))
	}
	if time.Duration(got.Retry) != 5*time.Second {
		t.Errorf("Retry = %s, ожидалось 5s", time.Duration(got.Retry))
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal вернул ошибку %v", err)
	}
	if !strings.Contains(string(encoded), `"timeout":"1m30s"`) {
		t.Errorf("Marshal = %s, ожидалась строковая длительность", encoded)
	}
}
