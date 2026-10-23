package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestThemesRetainFirst64InDocumentOrder(t *testing.T) {
	s := fixture(t).open(t, "unit", nil)
	var b strings.Builder
	b.WriteString(`{"version":1,"themes":{`)
	for i := 65; i > 0; i-- {
		if i < 65 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"t%02d":{"palette":{}}`, i)
	}
	b.WriteString(`}}`)
	writeConfig(t, s, []string{"themes.json"}, b.String())
	layers, err := s.Layers("themes", "")
	if err != nil || len(layers) != 1 {
		t.Fatalf("layers=%v err=%v", layers, err)
	}
	names := layers[0].OrderedMembers("/themes", layers[0].Doc["themes"].(Doc))
	if len(names) != 65 || names[0] != "t65" || names[63] != "t02" || names[64] != "t01" {
		t.Fatalf("lost theme source order: %v", names)
	}
}
