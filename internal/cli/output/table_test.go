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
		{"emoji, hot beverage 2615", "☕", 2},
		{"emoji, umbrella with rain 2614", "☔", 2},
		{"emoji, 1F300 block", "🌍", 2},
		{"emoji, 1F900 block", "🤖", 2},
		{"combining mark takes none", "é", 1},
		{"zero-width joiner takes none", "a‍b", 2},
		{"zero-width joiner sequence is one glyph", "👨‍👩‍👧", 2},
		{"word joiner takes none", "a\u2060b", 2},
		{"tag character takes none", "a\U000E0041b", 2},
		{"soft hyphen takes none", "a\u00adb", 2},
		{"ambiguous arrow is narrow", "→", 1},
		{"ambiguous degree is narrow", "°", 1},
		{"ambiguous box drawing is narrow", "─", 1},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayWidth(tc.s); got != tc.want {
				t.Fatalf("displayWidth(%q) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
}

// A wide cell pads by display columns, not runes: the literal text is what
// the terminal shows aligned.
func TestTableAlignsWideCells(t *testing.T) {
	for _, tc := range []struct{ first, want string }{
		{"🚀", "A     B\n🚀    x\nabcd  y\n"},
		{"🩰", "A     B\n🩰    x\nabcd  y\n"},
		{"⌚", "A     B\n⌚    x\nabcd  y\n"},
		{"☕", "A     B\n☕    x\nabcd  y\n"},
		{"漢字", "A     B\n漢字  x\nabcd  y\n"},
		{"한글", "A     B\n한글  x\nabcd  y\n"},
		{"e\u0301e\u0301", "A     B\ne\u0301e\u0301    x\nabcd  y\n"},
		{"a\u2060b", "A     B\na\u2060b   x\nabcd  y\n"},
	} {
		var b bytes.Buffer
		if err := WriteTable(&b, []string{"A", "B"}, [][]string{{tc.first, "x"}, {"abcd", "y"}}); err != nil {
			t.Fatal(err)
		}
		if b.String() != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.first, b.String(), tc.want)
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
	if strings.IndexFunc(strings.ReplaceAll(out, "\n", ""), unicode.IsControl) >= 0 {
		t.Fatalf("raw control character in %q", out)
	}
	for _, want := range []string{`"a\nb"`, `"\u001b[31mx"`, `"tab\tkey"`, "plain"} {
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
