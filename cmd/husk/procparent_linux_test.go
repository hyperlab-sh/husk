//go:build linux

package main

import "testing"

func TestParseStatPPID(t *testing.T) {
	cases := []struct {
		line string
		want int
		ok   bool
	}{
		{"1234 (husk) S 77 1234 1234 0 -1", 77, true},
		// comm may contain spaces and parentheses; only the last ')' ends it.
		{"1234 (a) b (c) R 42 1 1", 42, true},
		{"1234 (we ird)) S 9 1", 9, true},
		{"1234 husk S 77", 0, false},
		{"1234 (husk) S", 0, false},
		{"1234 (husk) S x", 0, false},
	}
	for _, c := range cases {
		got, err := parseStatPPID([]byte(c.line))
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("parseStatPPID(%q) = %d, %v; want %d ok=%v", c.line, got, err, c.want, c.ok)
		}
	}
}
