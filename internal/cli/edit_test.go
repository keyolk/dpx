package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitRef(t *testing.T) {
	cases := []struct {
		args      []string
		n         int
		parts     []string
		rest      []string
		wantError bool
	}{
		{args: []string{"p/c/K"}, n: 3, parts: []string{"p", "c", "K"}},
		{args: []string{"p/c", "K"}, n: 3, parts: []string{"p", "c", "K"}},
		{args: []string{"p", "c", "K"}, n: 3, parts: []string{"p", "c", "K"}},
		{args: []string{"p/c", "A=1", "B"}, n: 2, parts: []string{"p", "c"}, rest: []string{"A=1", "B"}},
		{args: []string{"p", "c", "A=1"}, n: 2, parts: []string{"p", "c"}, rest: []string{"A=1"}},
		{args: []string{"p/c/K/x"}, n: 3, wantError: true},
		{args: []string{"p/c"}, n: 3, wantError: true},
		{args: []string{"p//K"}, n: 3, wantError: true},
		// The config was forgotten and an assignment took its place.
		{args: []string{"p", "A=1"}, n: 2, wantError: true},
	}
	for _, c := range cases {
		parts, rest, err := splitRef(c.args, c.n)
		if c.wantError {
			if err == nil {
				t.Errorf("splitRef(%q) = %q, want error", c.args, parts)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitRef(%q): %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(parts, c.parts) || len(rest) != len(c.rest) || (len(rest) > 0 && !reflect.DeepEqual(rest, c.rest)) {
			t.Errorf("splitRef(%q) = %q %q, want %q %q", c.args, parts, rest, c.parts, c.rest)
		}
	}
}

func TestReadValueDropsOneNewline(t *testing.T) {
	cases := map[string]string{
		"v\n":               "v",
		"v":                 "v",
		"v\r\n":             "v",
		"-----END-----\n\n": "-----END-----\n",
		"line1\nline2\n":    "line1\nline2",
	}
	for in, want := range cases {
		got, err := readValue(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readValue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
