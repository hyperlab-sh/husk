package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestProfileServe(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewProfile(rec)
	port.Profiles = []contracts.ProfileInfo{{Name: "work", Source: "user", Active: true}}
	h := Profile{Port: port}

	var profiles []contracts.ProfileInfo
	reply := h.Serve(context.Background(), cliCaller, req(t, "profile.list", `{}`), nil)
	if reply.Error != nil || json.Unmarshal(reply.Result, &profiles) != nil || len(profiles) != 1 || profiles[0].Name != "work" {
		t.Fatalf("list = %s / %+v", reply.Result, reply.Error)
	}
	if reply := h.Serve(context.Background(), cliCaller, req(t, "profile.use", `{"name":"work"}`), nil); reply.Error != nil || string(reply.Result) != "{}" {
		t.Fatalf("use = %+v %s", reply.Error, reply.Result)
	}
	for _, method := range []string{"profile.list", "profile.use"} {
		errp := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), protocol.Message{Method: method}, nil))
		if errp.Code != protocol.CodeDenied {
			t.Fatalf("%s: code = %s, want denied", method, errp.Code)
		}
	}
	errp := replyErr(t, h.Serve(context.Background(), cliCaller, req(t, "profile.use", `{}`), nil))
	if errp.Code != protocol.CodeInvalidParams {
		t.Fatalf("use without name: %s", errp.Code)
	}
	if calls := rec.Methods(); !equalStrings(calls, []string{"Profile.List", "Profile.Use"}) {
		t.Fatalf("calls = %v (plugin denials must not reach the port)", calls)
	}
	errp = replyErr(t, h.Serve(context.Background(), cliCaller, protocol.Message{Method: "profile.poke"}, nil))
	if errp.Code != protocol.CodeUnknownMethod {
		t.Fatalf("profile.poke: %s, want unknown_method", errp.Code)
	}
}

func TestThemeServe(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewTheme(rec)
	port.Palette = protocol.Palette{Name: "dusk", Ink: "#eee"}
	port.CheckResult = contracts.ThemeCheckResult{Applied: false}
	h := Theme{Port: port}

	// theme.get is open to plugins too: they colour their own output.
	for _, caller := range []contracts.Caller{pluginCaller("files"), cliCaller} {
		got := decode(t, h.Serve(context.Background(), caller, req(t, "theme.get", `{}`), nil))
		pal, ok := got["palette"].(map[string]any)
		if !ok || pal["name"] != "dusk" || pal["ink"] != "#eee" {
			t.Fatalf("get = %v", got)
		}
	}
	check := decode(t, h.Serve(context.Background(), cliCaller, req(t, "theme.check", `{}`), nil))
	if _, has := check["applied"]; !has {
		t.Fatalf("check = %v", check)
	}
	errp := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), protocol.Message{Method: "theme.check"}, nil))
	if errp.Code != protocol.CodeDenied {
		t.Fatalf("plugin theme.check: %s, want denied", errp.Code)
	}
	if calls := rec.Methods(); !equalStrings(calls, []string{"Theme.Get", "Theme.Get", "Theme.Check"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestKeysServe(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewKeybinding(rec)
	port.Listing = contracts.KeyListing{Entries: []contracts.KeyEntry{{
		Keys: []string{"ctrl+t"}, Action: "files.tree", Plugin: "files",
		Source: protocol.Source{Layer: "user", Order: 3}, State: "effective",
	}}}
	h := Keys{Port: port}

	reply := h.Serve(context.Background(), pluginCaller("files"), req(t, "keys.list", `{}`), nil)
	var listing contracts.KeyListing
	if reply.Error != nil || json.Unmarshal(reply.Result, &listing) != nil || len(listing.Entries) != 1 {
		t.Fatalf("list = %s / %+v", reply.Result, reply.Error)
	}
	e := listing.Entries[0]
	if e.Action != "files.tree" || e.Source.Layer != "user" || e.State != "effective" || e.Source.Order != 3 {
		t.Fatalf("entry = %+v", e)
	}
	if reply := h.Serve(context.Background(), cliCaller, protocol.Message{Method: "keys.nothing"}, nil); reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("keys.nothing = %+v", reply.Error)
	}
}

func TestConfigServe(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewConfig(rec)
	port.Reports = []contracts.ReloadReport{{Service: "theme", Gen: 2, Applied: true}}
	h := Config{Port: port}

	got := decode(t, h.Serve(context.Background(), cliCaller, req(t, "config.reload", `{}`), nil))
	reports, ok := got["reports"].([]any)
	if !ok || len(reports) != 1 {
		t.Fatalf("reload = %v", got)
	}
	first, _ := reports[0].(map[string]any)
	if first["service"] != "theme" || first["gen"] != float64(2) || first["applied"] != true {
		t.Fatalf("report = %v", first)
	}
	errp := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), protocol.Message{Method: "config.reload"}, nil))
	if errp.Code != protocol.CodeDenied {
		t.Fatalf("plugin reload: %s, want denied", errp.Code)
	}
	if calls := rec.Methods(); !equalStrings(calls, []string{"Config.Reload"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestWindowLimiterTumblingWindow(t *testing.T) {
	clock := fakes.NewClock(time.Unix(1000, 0))
	l := NewWindowLimiter(clock, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("files") {
			t.Fatalf("call %d refused", i+1)
		}
	}
	if l.Allow("files") {
		t.Fatal("4th call allowed")
	}
	clock.Advance(999 * time.Millisecond)
	if l.Allow("files") {
		t.Fatal("allowed before the window closed")
	}
	clock.Advance(time.Millisecond)
	if !l.Allow("files") {
		t.Fatal("refused after the window reset")
	}
	// The window resets whole, not partially: three more, then refused.
	if !l.Allow("files") || !l.Allow("files") || l.Allow("files") {
		t.Fatal("budget did not reset whole")
	}
}

func TestWindowLimiterKeysAreIndependent(t *testing.T) {
	clock := fakes.NewClock(time.Unix(0, 0))
	l := NewWindowLimiter(clock, 1)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("key a budget wrong")
	}
	if !l.Allow("b") {
		t.Fatal("key b refused under a's budget")
	}
}

func TestWindowLimiterConstructorPanics(t *testing.T) {
	clock := fakes.NewClock(time.Unix(0, 0))
	for _, tc := range []struct {
		name  string
		clock contracts.Clock
		limit int
	}{{"nil clock", nil, 5}, {"zero limit", clock, 0}, {"negative limit", clock, -1}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: no panic", tc.name)
				}
			}()
			NewWindowLimiter(tc.clock, tc.limit)
		}()
	}
}

func TestWindowLimiterConcurrentAllow(t *testing.T) {
	clock := fakes.NewClock(time.Unix(0, 0))
	l := NewWindowLimiter(clock, 100)
	done := make(chan bool, 8)
	for g := 0; g < 8; g++ {
		go func() {
			for i := 0; i < 50; i++ {
				l.Allow("files")
			}
			done <- true
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	if got := l.window["files"].count; got != 100 {
		t.Fatalf("count = %d, want exactly the limit under concurrency", got)
	}
}
