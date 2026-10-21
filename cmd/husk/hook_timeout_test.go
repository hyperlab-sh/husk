package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func clockedRegistry(start time.Time) (*publicationRegistry, *time.Time) {
	r := newPublicationRegistry()
	c := &fakeClock{start}
	r.Clock = c
	return r, &c.t
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func TestHookTimeoutAnswersCallerPerWaitingSubscriber(t *testing.T) {
	r, now := clockedRegistry(time.Unix(1000, 0))
	_ = declareNamed(r.Hooks, "a", "a.hook")
	_ = subscribe(r.Hooks, "b", "a.hook")
	_ = subscribe(r.Hooks, "c", "a.hook")
	if _, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.hook", RequestID: "a.1"}); err != nil {
		t.Fatal(err)
	}
	// b answers in time; c stays silent.
	if _, err := r.hookResult("b", huskipc.HookResultPayload{ID: "a.hook", RequestID: "a.1"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(r.HookTimeout - time.Nanosecond)
	if out := r.expireHooks(*now); len(out) != 0 {
		t.Fatalf("expired before the deadline: %+v", out)
	}
	*now = now.Add(time.Nanosecond)
	out := r.expireHooks(*now)
	if len(out) != 1 || out[0].PluginID != "a" || out[0].Envelope.Type != "hook_result" {
		t.Fatalf("want one hook_result to the caller, got %+v", out)
	}
	res := out[0].Envelope.HookResult
	if res.ID != "a.hook" || res.RequestID != "a.1" || res.PluginID != "c" || res.Error != "timeout" {
		t.Fatalf("bad timeout result: %+v", res)
	}
	if len(r.Pending) != 0 {
		t.Fatal("expired call leaked")
	}
	if _, err := r.hookResult("c", huskipc.HookResultPayload{ID: "a.hook", RequestID: "a.1"}); err == nil {
		t.Fatal("late result accepted after timeout")
	}
	if again := r.expireHooks(now.Add(time.Hour)); len(again) != 0 {
		t.Fatal("timeout delivered twice")
	}
}

func TestHookTimeoutFreesRequestIDAndCapacity(t *testing.T) {
	r, now := clockedRegistry(time.Unix(0, 0))
	_ = declareNamed(r.Hooks, "a", "a.hook")
	_ = subscribe(r.Hooks, "quiet", "a.hook")
	call := huskipc.HookCallPayload{ID: "a.hook", RequestID: "a.1"}
	if _, err := r.callHook("a", call); err != nil {
		t.Fatal(err)
	}
	if _, err := r.callHook("a", call); err == nil {
		t.Fatal("duplicate pending accepted")
	}
	*now = now.Add(r.HookTimeout)
	r.expireHooks(*now)
	if _, err := r.callHook("a", call); err != nil {
		t.Fatalf("request id not reusable after timeout: %v", err)
	}
}

func TestHookPerSubscriberCapDoesNotBrickOthers(t *testing.T) {
	r, now := clockedRegistry(time.Unix(0, 0))
	_ = declareNamed(r.Hooks, "a", "a.slow")
	_ = declareNamed(r.Hooks, "a", "a.fast")
	_ = subscribe(r.Hooks, "quiet", "a.slow")
	_ = subscribe(r.Hooks, "live", "a.fast")
	for i := 0; i < maxPendingPerSubscriber; i++ {
		if _, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.slow", RequestID: fmt.Sprintf("a.%d", i)}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.slow", RequestID: "a.over"}); err == nil {
		t.Fatal("per-subscriber cap not enforced")
	}
	if r.pendingFor("quiet") != maxPendingPerSubscriber || r.pendingFor("live") != 0 {
		t.Fatalf("bad counts quiet=%d live=%d", r.pendingFor("quiet"), r.pendingFor("live"))
	}
	// Another hook with a different subscriber still works.
	if _, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.fast", RequestID: "a.fast1"}); err != nil {
		t.Fatalf("quiet subscriber bricked an unrelated hook: %v", err)
	}
	// Answering one frees a slot; so does expiry.
	if _, err := r.hookResult("quiet", huskipc.HookResultPayload{ID: "a.slow", RequestID: "a.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.slow", RequestID: "a.again"}); err != nil {
		t.Fatalf("slot not freed by result: %v", err)
	}
	*now = now.Add(r.HookTimeout)
	r.expireHooks(*now)
	if r.pendingFor("quiet") != 0 {
		t.Fatal("expiry did not free the subscriber")
	}
}

func TestHookTitleDescriptionRetained(t *testing.T) {
	r := newPublicationRegistry()
	if err := declareNamedInfo(r.Hooks, "a", "a.before", "Before refresh", "Notify extensions"); err != nil {
		t.Fatal(err)
	}
	if err := declareNamedInfo(r.Events, "a", "a.changed", "Changed", "Selection changed"); err != nil {
		t.Fatal(err)
	}
	_ = declareNamed(r.Hooks, "a", "a.bare")
	got := describe(r.Hooks)
	want := []PublicationInfo{
		{"a.bare", "a", "", ""},
		{"a.before", "a", "Before refresh", "Notify extensions"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hooks: got %+v want %+v", got, want)
	}
	if ev := describe(r.Events); len(ev) != 1 || ev[0] != (PublicationInfo{"a.changed", "a", "Changed", "Selection changed"}) {
		t.Fatalf("events: %+v", ev)
	}
}

func TestRegisterPublicationsKeepsHelloTitles(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	err := m.registerPublications(huskipc.HelloPayload{
		PluginID: "p",
		Hooks:    []huskipc.HookDeclarePayload{{ID: "p.h", Title: "H", Description: "hd"}},
		Events:   []huskipc.EventDeclarePayload{{ID: "p.e", Title: "E", Description: "ed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h := m.registry.Hooks["p.h"]; h.Title != "H" || h.Description != "hd" {
		t.Fatalf("hook info lost: %+v", h)
	}
	if e := m.registry.Events["p.e"]; e.Title != "E" || e.Description != "ed" {
		t.Fatalf("event info lost: %+v", e)
	}
}

func TestHookSweepRunsOnlyWhilePending(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	if m.startHookSweep() != nil {
		t.Fatal("idle host scheduled a sweep")
	}
	_ = declareNamed(m.registry.Hooks, "a", "a.hook")
	_ = subscribe(m.registry.Hooks, "b", "a.hook")
	_, _ = m.registry.callHook("a", huskipc.HookCallPayload{ID: "a.hook", RequestID: "a.1"})
	if m.startHookSweep() == nil || !m.hookSweepOn {
		t.Fatal("pending call did not schedule a sweep")
	}
	if m.startHookSweep() != nil {
		t.Fatal("sweep scheduled twice")
	}
	m.Update(hookSweepMsg{})
	if m.hookSweepOn == false && len(m.registry.Pending) == 1 {
		t.Fatal("sweep chain lapsed with a call still pending")
	}
}
