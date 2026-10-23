package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
)

// startupProbe returns the same failed initialization candidate from both entry
// points so the test detects either wrong dispatch or accidental publication.
type startupProbe struct {
	startupCalls atomic.Int32
	rebuildCalls atomic.Int32
	applies      atomic.Int32
	published    atomic.Bool
	beforeReturn func()
}

func (*startupProbe) Generation() Generation { return 0 }
func (s *startupProbe) Rebuild(_ context.Context, _ ReloadInput) (Candidate, Report, error) {
	s.rebuildCalls.Add(1)
	return s.failed()
}
func (s *startupProbe) RebuildStartup(_ context.Context, _ ReloadInput) (Candidate, Report, error) {
	s.startupCalls.Add(1)
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	return s.failed()
}
func (s *startupProbe) failed() (Candidate, Report, error) {
	// Deliberately set misleading flags: the coordinator must normalize a failed
	// report instead of copying the successful initialization Apply report.
	return startupProbeCandidate{s}, Report{Applied: true, Changed: true, Problems: []Problem{{Severity: "error", Code: "invalid_value", Key: "theme.name"}}}, c.ErrInvalid
}

type startupProbeCandidate struct{ service *startupProbe }

// Apply deliberately has no deadline guard. This pins the coordinator's own
// deadline check rather than letting a well-behaved candidate mask a bug.
func (p startupProbeCandidate) Apply() Report {
	p.service.applies.Add(1)
	p.service.published.Store(true)
	return Report{Applied: true, Changed: true}
}

func TestCoordinatorStartupInitialization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  Reason
		expired bool
	}{
		{"startup", ReasonStartup, false},
		{"cli", ReasonCLI, false},
		{"plugin", ReasonPlugin, false},
		{"profile", ReasonProfile, false},
		{"deadline", ReasonStartup, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := clock()
			probe := &startupProbe{}
			if tc.expired {
				probe.beforeReturn = func() {
					// Advance Now without delivering the timer: the prepared result must
					// reach the coordinator, which must check the deadline before Apply.
					clk.mu.Lock()
					clk.now = clk.now.Add(3 * time.Second)
					clk.mu.Unlock()
				}
			}
			set := fakeServices()
			set["settings"] = probe
			theme := set["theme"].(*fakeService)
			co, err := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clk})
			if err != nil {
				t.Fatal(err)
			}
			defer co.Close()
			reports, err := co.Reload(t.Context(), tc.reason)
			if len(reports) != 5 {
				t.Fatalf("reports=%+v err=%v", reports, err)
			}
			startup := tc.reason == ReasonStartup
			if got := probe.startupCalls.Load(); got != map[bool]int32{true: 1, false: 0}[startup] {
				t.Fatalf("startup calls=%d", got)
			}
			if got := probe.rebuildCalls.Load(); got != map[bool]int32{true: 0, false: 1}[startup] {
				t.Fatalf("ordinary rebuild calls=%d", got)
			}
			wantPublish := startup && !tc.expired
			if probe.applies.Load() != map[bool]int32{true: 1, false: 0}[wantPublish] || probe.published.Load() != wantPublish {
				t.Fatalf("applies=%d published=%v", probe.applies.Load(), probe.published.Load())
			}
			settingsReport, themeReport := reports[1], reports[2]
			if settingsReport.Applied || settingsReport.Changed || settingsReport.Gen != 0 {
				t.Fatalf("settings report=%+v", settingsReport)
			}
			if theme.calls.Load() != 0 || themeReport.Applied || themeReport.Changed || themeReport.Gen != 0 {
				t.Fatalf("theme calls=%d report=%+v", theme.calls.Load(), themeReport)
			}
			if tc.expired {
				var coded *c.Error
				if !errors.As(err, &coded) || coded.Code != "timeout" || coded.Reason != "reload_deadline" {
					t.Fatalf("deadline error=%v", err)
				}
				for _, report := range reports[1:] {
					if report.Applied || !hasProblem(report.Problems, "timed_out", "error") {
						t.Fatalf("expired report=%+v", report)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !hasProblem(settingsReport.Problems, "invalid_value", "error") || !hasProblem(themeReport.Problems, "settings_unchanged", "warning") {
					t.Fatalf("settings=%+v theme=%+v", settingsReport, themeReport)
				}
			}
		})
	}
}

func TestCoordinatorStartupCapabilityIsNotServiceNameSpecific(t *testing.T) {
	set := fakeServices()
	probe := &startupProbe{}
	set["keybinding"] = probe
	co, err := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clock()})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	reports, err := co.Reload(t.Context(), ReasonStartup)
	if err != nil {
		t.Fatal(err)
	}
	if probe.startupCalls.Load() != 1 || probe.rebuildCalls.Load() != 0 || probe.applies.Load() != 1 || !probe.published.Load() {
		t.Fatalf("startup=%d ordinary=%d applies=%d", probe.startupCalls.Load(), probe.rebuildCalls.Load(), probe.applies.Load())
	}
	if report := reports[3]; report.Applied || report.Changed || report.Gen != 0 || !hasProblem(report.Problems, "invalid_value", "error") {
		t.Fatalf("report=%+v", report)
	}
}
