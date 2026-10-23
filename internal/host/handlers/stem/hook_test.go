package stem

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hook.call is plugin-only (the CLI is not at any owner's point); hook.list
// admits CLI and plugin callers. Everything else, and the CLI on hook.call,
// is denied before the port.
func TestHookAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		caller contracts.Caller
		denied bool
	}{
		{"cli call", protocol.MethodHookCall, cli, true},
		{"stem call", protocol.MethodHookCall, stemSide, true},
		{"unattributed call", protocol.MethodHookCall, nobody, true},
		{"plugin call", protocol.MethodHookCall, plugin, false},
		{"cli list", protocol.MethodHookList, cli, false},
		{"plugin list", protocol.MethodHookList, plugin, false},
		{"stem list", protocol.MethodHookList, stemSide, true},
		{"unattributed list", protocol.MethodHookList, nobody, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fakes.NewRecorder()
			reply := Hook{Port: fakes.NewHooks(rec)}.Serve(ctx, tc.caller, req(tc.method, `{"hook":"a.before-save"}`), nil)
			if !tc.denied {
				if reply.Error != nil {
					t.Fatalf("error = %+v", reply.Error)
				}
				return
			}
			wantCode(t, reply, protocol.CodeDenied)
			if calls := rec.Methods(); len(calls) != 0 {
				t.Fatalf("denied caller reached the port: %v", calls)
			}
		})
	}
}

func callHook(t *testing.T, o contracts.HookOutcome) string {
	t.Helper()
	port := fakes.NewHooks(nil)
	port.Outcome = o
	reply := Hook{Port: port}.Serve(ctx, plugin, req(protocol.MethodHookCall, `{"hook":"atlas.h","data":{}}`), nil)
	if reply.Error != nil {
		t.Fatalf("error = %+v", reply.Error)
	}
	return string(reply.Result)
}

// The outcomes below are the examples of design/hooks.md, "Wire shapes".
func TestHookCallOutcomes(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	for _, tc := range []struct {
		name string
		out  contracts.HookOutcome
		want string
	}{
		{
			"transform: data always, pass-through entry without data",
			contracts.HookOutcome{Kind: "transform", Data: raw(`{"path":"/lists/todo.md","viewer":"markdown"}`), Results: []contracts.HookResult{
				{Plugin: "preview", Data: raw(`{"path":"/lists/todo.md","viewer":"markdown"}`)},
				{Plugin: "stats"},
			}},
			`{"kind":"transform","data":{"path":"/lists/todo.md","viewer":"markdown"},"results":[{"plugin_id":"preview","data":{"path":"/lists/todo.md","viewer":"markdown"}},{"plugin_id":"stats"}]}`,
		},
		{
			"transform: empty replacement keeps data {}",
			contracts.HookOutcome{Kind: "transform", Data: raw(`{}`), Results: []contracts.HookResult{{Plugin: "preview", Data: raw(`{}`)}}},
			`{"kind":"transform","data":{},"results":[{"plugin_id":"preview","data":{}}]}`,
		},
		{
			"transform: no subscribers still carries data",
			contracts.HookOutcome{Kind: "transform"},
			`{"kind":"transform","data":{},"results":[]}`,
		},
		{
			"transform: null data is never emitted",
			contracts.HookOutcome{Kind: "transform", Data: raw(`null`), Results: []contracts.HookResult{{Plugin: "p", Data: raw(`null`)}}},
			`{"kind":"transform","data":{},"results":[{"plugin_id":"p"}]}`,
		},
		{
			"collect: items per entry, absent when none, leg error verbatim",
			contracts.HookOutcome{Kind: "collect", Allowed: true, Results: []contracts.HookResult{
				{Plugin: "preview", Items: raw(`[{"label":"Preview","action":"preview.open"}]`)},
				{Plugin: "stats"},
				{Plugin: "index", Err: contracts.NewError(protocol.CodeTimeout, "no reply in 10s", nil)},
			}},
			`{"kind":"collect","results":[{"plugin_id":"preview","items":[{"label":"Preview","action":"preview.open"}]},{"plugin_id":"stats"},{"plugin_id":"index","error":{"code":"timeout","message":"no reply in 10s"}}]}`,
		},
		{
			"gate: allowed",
			contracts.HookOutcome{Kind: "gate", Allowed: true, Results: []contracts.HookResult{{Plugin: "index"}, {Plugin: "preview"}}},
			`{"kind":"gate","allowed":true,"vetoes":[],"results":[{"plugin_id":"index"},{"plugin_id":"preview"}]}`,
		},
		{
			"gate: vetoed",
			contracts.HookOutcome{Kind: "gate", Vetoes: []contracts.HookVeto{{Plugin: "index", Reason: "index write in progress"}}, Results: []contracts.HookResult{
				{Plugin: "index", Veto: true, Reason: "index write in progress"},
				{Plugin: "preview"},
			}},
			`{"kind":"gate","allowed":false,"vetoes":[{"plugin_id":"index","reason":"index write in progress"}],"results":[{"plugin_id":"index","veto":true,"reason":"index write in progress"},{"plugin_id":"preview"}]}`,
		},
		{
			"leg error carries data verbatim",
			contracts.HookOutcome{Kind: "collect", Allowed: true, Results: []contracts.HookResult{
				{Plugin: "stats", Err: contracts.NewError(protocol.CodeCancelled, "plugin exited", map[string]any{"reason": "plugin_exited"})},
			}},
			`{"kind":"collect","results":[{"plugin_id":"stats","error":{"code":"cancelled","message":"plugin exited","data":{"reason":"plugin_exited"}}}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callHook(t, tc.out); got != tc.want {
				t.Fatalf("result =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestHookCall(t *testing.T) {
	t.Run("refusal is an error reply with no results", func(t *testing.T) {
		port := fakes.NewHooks(nil)
		port.Err = contracts.NewError(protocol.CodeDenied, `only the owner of "a.h" may fire it`, nil)
		reply := Hook{Port: port}.Serve(ctx, plugin, req(protocol.MethodHookCall, `{"hook":"a.h"}`), nil)
		wantCode(t, reply, protocol.CodeDenied)
		if reply.Result != nil {
			t.Fatalf("refusal carries a result: %s", reply.Result)
		}
	})
	t.Run("busy keeps its data", func(t *testing.T) {
		port := fakes.NewHooks(nil)
		port.Err = contracts.NewError(protocol.CodeBusy, "too many", map[string]any{"reason": "hook_in_flight"})
		reply := Hook{Port: port}.Serve(ctx, plugin, req(protocol.MethodHookCall, `{"hook":"a.h"}`), nil)
		wantCode(t, reply, protocol.CodeBusy)
		if string(reply.Error.Data) != `{"reason":"hook_in_flight"}` {
			t.Fatalf("data = %s", reply.Error.Data)
		}
	})
	t.Run("missing hook", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, plugin, req(protocol.MethodHookCall, `{}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("non-object data", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, plugin,
			req(protocol.MethodHookCall, `{"hook":"h","data":7}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("unknown method", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, plugin, req("hook.nope", ""), nil)
		wantCode(t, reply, protocol.CodeUnknownMethod)
	})
}

func TestHookList(t *testing.T) {
	t.Run("lists every hook; empty title and description are omitted", func(t *testing.T) {
		port := fakes.NewHooks(nil)
		port.Infos = []contracts.HookInfo{
			{ID: "notes.resolve-viewer", Owner: "notes", Kind: "transform", TimeoutMS: 2000, Title: "Resolve viewer", Description: "Which viewer opens a path",
				Subscribers: []contracts.HookSubscriber{{Plugin: "preview", Priority: 5, Live: true}, {Plugin: "stats", Priority: 10}}},
			{ID: "notes.after-save", Owner: "notes", Kind: "collect", TimeoutMS: 1500, Subscribers: nil},
		}
		reply := Hook{Port: port}.Serve(ctx, cli, req(protocol.MethodHookList, `{}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		want := `{"hooks":[{"id":"notes.resolve-viewer","owner":"notes","kind":"transform","timeout_ms":2000,"title":"Resolve viewer","description":"Which viewer opens a path","subscribers":[{"plugin_id":"preview","priority":5,"live":true},{"plugin_id":"stats","priority":10,"live":false}]},{"id":"notes.after-save","owner":"notes","kind":"collect","timeout_ms":1500,"subscribers":[]}]}`
		if string(reply.Result) != want {
			t.Fatalf("result =\n%s\nwant\n%s", reply.Result, want)
		}
	})
	t.Run("no hooks is an empty array", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, plugin, req(protocol.MethodHookList, ``), nil)
		if string(reply.Result) != `{"hooks":[]}` {
			t.Fatalf("result = %s", reply.Result)
		}
	})
	t.Run("port error maps", func(t *testing.T) {
		port := fakes.NewHooks(nil)
		port.Err = contracts.NewError(protocol.CodeUnavailable, "down", nil)
		wantCode(t, Hook{Port: port}.Serve(ctx, cli, req(protocol.MethodHookList, ``), nil), protocol.CodeUnavailable)
	})
}
