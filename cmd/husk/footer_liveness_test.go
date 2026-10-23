package main

import (
	"slices"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

func TestFooterHintFollowsLiveness(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.Acts = []contracts.PluginAction{{Plugin: "owner", ID: "owner.toggle"}}
	fe.HintsFn = func(contracts.KeyContext) []contracts.KeyHint {
		return []contracts.KeyHint{{Keys: []string{"f2"}, Action: "owner.toggle", Text: "toggle"}}
	}
	for _, live := range []bool{true, false, true} {
		m.plugins["owner"] = live
		if got := slices.Contains(hintKeys(m), "F2"); got != live {
			t.Fatalf("live=%v, hint shown=%v", live, got)
		}
	}
	// A prefix is not ownership, and declared rail items need not be live.
	fe.Acts = nil
	if m.liveHint("owner.toggle") {
		t.Fatal("unknown action advertised")
	}
	m.declared = []contracts.RailItem{{ID: "owner.panel", Plugin: "owner"}}
	m.plugins["owner"] = false
	if !m.liveHint("owner.panel") || !m.liveHint("husk.home") {
		t.Fatal("host or declared rail item filtered")
	}
}

func TestFooterHintFilterAfterCap(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	m.plugins["owner"] = true
	var hints []contracts.KeyHint
	for i, k := range []string{"f2", "f3", "f4", "f5", "f6"} {
		id := "owner." + k
		hints = append(hints, contracts.KeyHint{Keys: []string{k}, Action: id, Text: k})
		if i != 1 {
			fe.Acts = append(fe.Acts, contracts.PluginAction{Plugin: "owner", ID: id})
		}
	}
	fe.HintsFn = func(contracts.KeyContext) []contracts.KeyHint { return hints }
	keys := hintKeys(m)
	for _, k := range []string{"F2", "F4", "F5"} {
		if !slices.Contains(keys, k) {
			t.Fatalf("missing %s: %v", k, keys)
		}
	}
	for _, k := range []string{"F3", "F6"} {
		if slices.Contains(keys, k) {
			t.Fatalf("filtered or fifth hint %s shown: %v", k, keys)
		}
	}
}
