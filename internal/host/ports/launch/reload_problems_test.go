package launch

import (
	"context"
	"errors"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services"
)

// A failed reload names the failing service through the reports' own coded
// problems; the aggregate error adds no second problem when reports exist.
func TestReloadProblemsNameTheFailingService(t *testing.T) {
	reports := []services.Report{
		{Service: "settings", Applied: true},
		{Service: "keybinding", Applied: false, Problems: []services.Problem{{Severity: "error", Code: "invalid_value", Layer: "user", Key: "bind", Message: "bad chord"}}},
		{Service: "theme", Applied: false, Problems: []services.Problem{{Severity: "error", Code: "candidate_superseded", Message: "stale"}}},
	}
	for name, err := range map[string]error{
		"incomplete": errors.New("service reload incomplete"),
		"deadline":   context.DeadlineExceeded,
		"cancelled":  context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			applied, problems := reloadProblems(reports, err)
			if applied {
				t.Fatal("applied despite a failed service")
			}
			if len(problems) != 2 {
				t.Fatalf("problems = %+v, want exactly the two reports' own", problems)
			}
			if p := problems[0]; p.Code != "invalid_value" || p.Layer != "user" || p.Message != "keybinding: bad chord" {
				t.Errorf("keybinding problem = %+v", p)
			}
			if p := problems[1]; p.Code != "candidate_superseded" || p.Message != "theme: stale" {
				t.Errorf("theme problem = %+v", p)
			}
		})
	}
	if ok, ps := reloadProblems([]services.Report{{Service: "settings", Applied: true}}, nil); !ok || len(ps) != 0 {
		t.Errorf("clean reload = %v %v", ok, ps)
	}
}

// With no reports the context ended before the flight finished, and only then
// is the aggregate problem emitted: cancelled or timed_out by the context error.
func TestReloadProblemsContextEndedBeforeAnyReport(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"cancelled": {context.Canceled, "cancelled"},
		"deadline":  {context.DeadlineExceeded, "timed_out"},
	} {
		t.Run(name, func(t *testing.T) {
			applied, problems := reloadProblems(nil, tc.err)
			if applied || len(problems) != 1 || problems[0].Code != tc.code || problems[0].Layer != "services" || problems[0].Severity != "error" {
				t.Fatalf("applied=%v problems=%+v, want one error %q", applied, problems, tc.code)
			}
		})
	}
}
