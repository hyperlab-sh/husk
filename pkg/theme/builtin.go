package theme

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

//go:embed themes.json
var builtInDocument []byte

// BuiltIn returns a fresh decode of the lowest themes layer. Callers may mutate
// it without changing the embedded document or another caller's view.
func BuiltIn() map[string]any {
	var doc map[string]any
	if err := json.Unmarshal(builtInDocument, &doc); err != nil {
		panic("invalid built-in themes: " + err.Error())
	}
	return doc
}

// BuiltInNames returns the built-in theme names in presentation order.
func BuiltInNames() []string {
	return []string{"charm", "charm-light", "dracula", "base16", "catppuccin-mocha", "catppuccin-latte", "rose-pine", "rose-pine-moon", "rose-pine-dawn"}
}

// ResolveValue validates a themes-document role and resolves an ANSI index to
// its xterm default. The returned index is -1 for a hex value, or 0..15 for an
// index. Settings overrides use Overlay's stricter hex-only grammar instead.
func ResolveValue(value any) (hex string, index int, err error) {
	if s, ok := value.(string); ok {
		if _, err := RelativeLuminance(s); err == nil {
			return strings.ToLower(s), -1, nil
		}
	}
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case int:
		number = float64(v)
	case json.Number:
		var parseErr error
		number, parseErr = v.Float64()
		if parseErr != nil {
			return "", -1, fmt.Errorf("want #RRGGBB or integer 0..15")
		}
	default:
		return "", -1, fmt.Errorf("want #RRGGBB or integer 0..15")
	}
	if number < 0 || number > 15 || math.Trunc(number) != number {
		return "", -1, fmt.Errorf("want #RRGGBB or integer 0..15")
	}
	xterm := [...]string{
		"#000000", "#800000", "#008000", "#808000",
		"#000080", "#800080", "#008080", "#c0c0c0",
		"#808080", "#ff0000", "#00ff00", "#ffff00",
		"#0000ff", "#ff00ff", "#00ffff", "#ffffff",
	}
	return xterm[int(number)], int(number), nil
}
