package output

import (
	"bytes"
	"strings"
	"testing"
	"unicode"
)

func TestDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		want int
	}{
		{"ascii", "abc", 3},
		{"cjk ideograph", "漢", 2},
		{"hangul syllable", "한", 2},
		{"hangul jamo", "ᄀ", 2},
		{"fullwidth form", "Ａ", 2},
		{"emoji, transport block 1F680", "🚀", 2},
		{"emoji, extended-A block 1FA70", "🩰", 2},
		{"emoji, watch 231A", "⌚", 2},
		{"emoji, hot beverage 2614", "☕", 2},
		{"emoji, 1F300 block", "🌍", 2},
		{"emoji, 1F900 block", "🤖", 2},
		{"combining mark takes none", "é", 1},
		{"zero-width joiner takes none", "a‍b", 2},
		{"zero-width joiner sequence is one glyph", "👨‍👩‍👧", 2},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayWidth(tc.s); got != tc.want {
				t.Fatalf("displayWidth(%q) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
}

// A wide cell pads by display columns, not runes: the next column starts at
// the same display column on every row.
func TestTableAlignsWideCells(t *testing.T) {
	for _, first := range []string{"🚀", "🩰", "⌚", "☕", "漢字", "한글", "éé"} {
		var b bytes.Buffer
		if err := WriteTable(&b, []string{"A", "B"}, [][]string{{first, "x"}, {"abcd", "y"}}); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
		w := max(displayWidth(first), displayWidth("abcd"), 1) + 2
		for _, l := range lines {
			i := strings.LastIndexAny(l, "Bxy")
			if got := displayWidth(l[:i]); got != w {
				t.Fatalf("%q: second column of %q starts at column %d, want %d", first, l, got, w)
			}
		}
	}
}

func TestGenericEscapesObjectKeys(t *testing.T) {
	var b bytes.Buffer
	if err := Generic(&b, []byte(`{"a\nb":1,"\u001b[31mx":2,"tab\tkey":3,"plain":4}`)); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(lines) != 4 {
		t.Fatalf("want 4 rows, got %d: %q", len(lines), out)
	}
	if i := strings.IndexFunc(strings.TrimSuffix(out, "\n"), unicode.IsControl); i >= 0 && out[i] != '\n' {
		t.Fatalf("raw control byte %q in %q", out[i], out)
	}
	for _, want := range []string{`"a\nb"  1`, `"\u001b[31mx"  2`, `"tab\tkey"  3`, "plain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}

func TestGenericEscapesRecordHeaders(t *testing.T) {
	var b bytes.Buffer
	if err := Generic(&b, []byte(`[{"a\nb":1}]`)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), "\n") != 2 {
		t.Fatalf("a header with a newline broke the table: %q", b.String())
	}
}
