package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

func TestRecoverableHealthOverflowPublishesBoundedSummary(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	// Drive both inventory and diagnostic text far beyond the core slice cap.
	for i := 0; i < 60; i++ {
		s.diagnostic(Problem{Code: "recoverable", Message: strings.Repeat("\x01", state.CoreMaxBytes), File: strings.Repeat("path", 2000)}, strings.Repeat("quarantine-path", 1000))
	}
	st := attach(t, s)
	entry := st.Snapshot().Slices[state.StoreNamespace]
	if entry.Bytes > state.CoreMaxBytes {
		t.Fatal("health exceeds cap", entry.Bytes)
	}
	var summary health
	if err := json.Unmarshal(entry.Value, &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.Truncated || len(summary.Diagnostics) != 1 || len(summary.Quarantined) != 0 {
		t.Fatal("oversized health did not degrade explicitly")
	}
	s.mu.Lock()
	retained, inventory := len(s.health.Diagnostics), len(s.health.Quarantined)
	s.mu.Unlock()
	if retained != 50 || inventory != 60 {
		t.Fatal("wire trimming discarded internal history", retained, inventory)
	}
	before := entry.Revision
	s.diagnostic(Problem{Code: "newest", Message: "latest warning"}, "")
	drainPosts(e)
	entry = st.Snapshot().Slices[state.StoreNamespace]
	if entry.Revision <= before || entry.Bytes > state.CoreMaxBytes {
		t.Fatal("recoverable warning failed to publish")
	}
	if err := json.Unmarshal(entry.Value, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Diagnostics[len(summary.Diagnostics)-1].Code != "newest" {
		t.Fatal("newest warning missing")
	}
}
func TestHealthTrimsOnlyOldestQuarantinePathsWhenEnough(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	for i := 0; i < 50; i++ {
		s.diagnostic(Problem{Code: "small", Message: "retain all fifty"}, strings.Repeat("path", 1024))
	}
	st := attach(t, s)
	entry := st.Snapshot().Slices[state.StoreNamespace]
	var summary health
	if err := json.Unmarshal(entry.Value, &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.Truncated || len(summary.Diagnostics) != 50 || len(summary.Quarantined) == 0 || len(summary.Quarantined) >= 50 {
		t.Fatal("trimming order incorrect", len(summary.Diagnostics), len(summary.Quarantined))
	}
}
