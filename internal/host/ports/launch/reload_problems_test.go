package launch

import (
	"errors"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services"
)

// A failed reload names the failing service instead of only the aggregate.
func TestReloadProblemsNameTheFailingService(t *testing.T) {
	reports := []services.Report{
		{Service: "settings", Applied: true},
		{Service: "keybinding", Applied: false, Problems: []services.Problem{{Severity: "error", Layer: "user", Key: "bind", Message: "bad chord"}}},
		{Service: "theme", Applied: false},
	}
	applied, problems := reloadProblems(reports, errors.New("service reload incomplete"))
	if applied {
		t.Fatal("applied despite a failed service")
	}
	var got []string
	for _, p := range problems {
		got = append(got, p.Message)
	}
	joined := strings.Join(got, "|")
	for _, want := range []string{"keybinding: bad chord", "theme: not applied", "service reload incomplete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems %q lack %q", joined, want)
		}
	}
	if strings.Contains(joined, "settings") {
		t.Errorf("an applied service was reported: %q", joined)
	}
	if ok, ps := reloadProblems([]services.Report{{Service: "settings", Applied: true}}, nil); !ok || len(ps) != 0 {
		t.Errorf("clean reload = %v %v", ok, ps)
	}
}
