package surfaces

import (
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestOwnNameMatchesStateGrammar pins ownName to State's grammar: the
// name segment carries none of the separator or control characters the
// wire forbids, with the same codes State's own id validation produces —
// invalid_params for malformed, denied for a foreign namespace.
func TestOwnNameMatchesStateGrammar(t *testing.T) {
	const plugin = "alpha.files"
	valid := []string{
		"alpha.files.item",
		"alpha.files.x",
	}
	for _, id := range valid {
		if err := ownName(plugin, id); err != nil {
			t.Errorf("ownName(%q) = %v, want nil", id, err)
		}
	}

	malformed := map[string]string{ // id -> want message fragment
		"":                   "id is required",
		".item":              "must be",
		"alpha.files.":       "must be",
		"alpha.files.a b":    "forbidden separator",
		"alpha.files.a\tb":   "forbidden separator",
		"alpha.files.a\nb":   "forbidden separator",
		"alpha.files.a\x1bb": "forbidden separator",
		"alpha.files.a;b":    "forbidden separator",
		"alpha.files.a=b":    "forbidden separator",
		`alpha.files.a\b`:    "forbidden separator",
	}
	for id, frag := range malformed {
		err := ownName(plugin, id)
		if err == nil {
			t.Errorf("ownName(%q) = nil, want invalid_params", id)
			continue
		}
		cErr, ok := err.(*Error)
		if !ok {
			t.Errorf("ownName(%q) = %T, want *Error", id, err)
			continue
		}
		if cErr.Code != protocol.CodeInvalidParams {
			t.Errorf("ownName(%q) code = %v, want invalid_params", id, cErr.Code)
		}
		if !strings.Contains(cErr.Message, frag) {
			t.Errorf("ownName(%q) message = %q, want it to mention %q", id, cErr.Message, frag)
		}
	}

	err := ownName("beta.notes", "alpha.files.item")
	if err == nil {
		t.Fatal("ownName(foreign) = nil, want denied")
	}
}
