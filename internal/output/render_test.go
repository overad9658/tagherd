package output

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

type item struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

var (
	data = []item{{"a|b", 1}, {"c", 22}}
	rows = Rows{
		Headers: []string{"NAME", "N"},
		Rows:    [][]string{{"a|b", "1"}, {"c", "22"}},
		Footer:  []string{"2 items."},
	}
)

func TestRender(t *testing.T) {
	tests := map[Format]string{
		Table:    "NAME  N\na|b   1\nc     22\n\n2 items.\n",
		JSON:     "[\n  {\n    \"name\": \"a|b\",\n    \"n\": 1\n  },\n  {\n    \"name\": \"c\",\n    \"n\": 22\n  }\n]\n",
		NDJSON:   "{\"name\":\"a|b\",\"n\":1}\n{\"name\":\"c\",\"n\":22}\n",
		CSV:      "NAME,N\na|b,1\nc,22\n",
		Markdown: "| NAME | N |\n| --- | --- |\n| a\\|b | 1 |\n| c | 22 |\n\n2 items.\n",
	}
	for f, want := range tests {
		var b bytes.Buffer
		if err := Render(&b, Options{Format: f}, data, rows); err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Errorf("%s:\n%q\nwant\n%q", f, b.String(), want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	if f, err := ParseFormat("JSON"); err != nil || f != JSON {
		t.Errorf("ParseFormat(JSON) = %v, %v", f, err)
	}
	if _, err := ParseFormat("yaml"); !errors.Is(err, ErrUsage) {
		t.Errorf("unknown format: err = %v", err)
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{Usagef("bad flag"), ExitUsage},
		{errors.New("anything else"), ExitUsage},
		{&ExitError{Code: ExitFindings, Err: errors.New("findings")}, ExitFindings},
		{fmt.Errorf("wrapped: %w", &ExitError{Code: ExitIncomplete, Err: errors.New("x")}), ExitIncomplete},
	}
	for _, tc := range tests {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
