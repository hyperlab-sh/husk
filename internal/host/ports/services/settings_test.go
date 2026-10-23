package services

import (
	"context"
	"testing"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

var (
	aCaller  = host.Caller{Kind: host.CallerPlugin, PluginID: "a"}
	abCaller = host.Caller{Kind: host.CallerPlugin, PluginID: "a.b"}
)

// declaredSettingsInput declares one setting each for plugins a, a.b (dotted
// ids: a prefix is not an identity) and files, with values doc.
func declaredSettingsInput(t *testing.T, values string) services.ReloadInput {
	t.Helper()
	return services.ReloadInput{
		Doc: store.Resolved{Doc: doc(t, values), Origins: map[string]store.Layer{}},
		Declared: map[store.DocName]map[string]store.Doc{
			store.DeclaredPlugins: {
				"a":     doc(t, `{"id":"a","settings_declared":[{"key":"plugins.a.mode","type":"string","default":"auto"}]}`),
				"a.b":   doc(t, `{"id":"a.b","settings_declared":[{"key":"plugins.a.b.mode","type":"string","default":"auto"}]}`),
				"files": doc(t, `{"id":"files","settings_declared":[{"key":"plugins.files.tree","type":"bool","default":false}]}`),
			},
		},
	}
}

// codedOf returns the error's protocol code and data.reason.
func codedOf(t *testing.T, err error) (string, string) {
	t.Helper()
	ce, ok := err.(host.CodedError)
	if !ok {
		t.Fatalf("error %v (%T) carries no protocol code", err, err)
	}
	reason := ""
	if de, ok := err.(host.DataError); ok {
		reason, _ = de.Data()["reason"].(string)
	}
	return ce.Code(), reason
}

func TestSettingsGetOwnershipMatrix(t *testing.T) {
	svc := newSet(settings.Options{Schema: []settings.Entry{{Key: "dist.rail", Type: "string", Default: "", Owner: "dist", Apply: "live", Public: true}, {Key: "dist.hidden", Type: "string", Default: "", Owner: "dist", Apply: "live"}}})
	in := declaredSettingsInput(t, `{"plugins":{"a":{"mode":"alpha"},"a.b":{"mode":"beta"},"files":{"tree":true}}}`)
	applyTo(t, svc, in)
	port := Settings{Svc: svc, Inputs: staticInputs{in: in}}

	cases := []struct {
		name   string
		caller host.Caller
		key    string
		value  any    // expected typed value on success
		code   string // expected error code, empty on success
		reason string // expected data.reason on error
	}{
		{name: "own declared key", caller: aCaller, key: "plugins.a.mode", value: "alpha"},
		// A dotted prefix is not an identity: a must not read a.b's key,
		// and the refusal is the same unknown_setting as an absent key.
		{name: "sibling under prefix", caller: aCaller, key: "plugins.a.b.mode", code: "not_found", reason: "unknown_setting"},
		{name: "exact dotted owner", caller: abCaller, key: "plugins.a.b.mode", value: "beta"},
		{name: "foreign owner", caller: filesCaller, key: "plugins.other.key", code: "not_found", reason: "unknown_setting"},
		{name: "public dist default", caller: filesCaller, key: "dist.rail", value: ""},
		{name: "non-public dist key", caller: filesCaller, key: "dist.hidden", code: "not_found", reason: "unknown_setting"},
		{name: "non-public host key", caller: filesCaller, key: "husk.layout.rail", code: "not_found", reason: "unknown_setting"},
		{name: "absent husk key", caller: filesCaller, key: "husk.nope", code: "not_found", reason: "unknown_setting"},
		{name: "cli reads any owner", caller: userCaller, key: "plugins.files.tree", value: true},
		{name: "cli absent key", caller: userCaller, key: "nope", code: "not_found", reason: "unknown_setting"},
	}
	for _, tc := range cases {
		v, _, err := port.Get(context.Background(), tc.caller, tc.key)
		if tc.code == "" {
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if v != tc.value {
				t.Fatalf("%s: value = %#v, want %#v", tc.name, v, tc.value)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: got value %#v, want %s/%s", tc.name, v, tc.code, tc.reason)
		}
		if code, reason := codedOf(t, err); code != tc.code || reason != tc.reason {
			t.Fatalf("%s: %s/%s, want %s/%s", tc.name, code, reason, tc.code, tc.reason)
		}
	}
	// The host's Stem engine is trusted and reads like the user.
	if v, _, err := port.Get(context.Background(), stemHost, "plugins.files.tree"); err != nil || v != true {
		t.Fatalf("stem: %v %v", v, err)
	}
}

func TestSettingsListSchemaValidate(t *testing.T) {
	svc := newSet(settings.Options{})
	in := declaredSettingsInput(t, `{"plugins":{"files":{"tree":true}}}`)
	applyTo(t, svc, in)
	port := Settings{Svc: svc, Inputs: staticInputs{in: in}}

	rows, err := port.List(context.Background(), userCaller)
	values := map[string]any{}
	for _, r := range rows {
		values[r.Key] = r.Value
	}
	if err != nil || values["plugins.files.tree"] != true || values["plugins.a.mode"] != "auto" {
		t.Fatalf("list = %#v err=%v (set values plus schema defaults)", rows, err)
	}
	if _, err := port.List(context.Background(), filesCaller); err == nil {
		t.Fatal("plugin settings.list allowed")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_only" {
		t.Fatalf("plugin list: %s/%s", code, reason)
	}

	entries, err := port.Schema(context.Background(), userCaller, "plugin:a")
	if err != nil || len(entries) != 1 || entries[0].Key != "plugins.a.mode" || entries[0].Owner != "plugin:a" {
		t.Fatalf("schema = %+v err=%v", entries, err)
	}
	all, err := port.Schema(context.Background(), userCaller, "")
	if err != nil || len(all) < 4 { // three declared plus the built-in husk schema
		t.Fatalf("schema all = %d entries err=%v", len(all), err)
	}
	if _, err := port.Schema(context.Background(), filesCaller, ""); err == nil {
		t.Fatal("plugin settings.schema allowed")
	}

	// settings.validate dry-runs fresh input through the Inputs seam.
	rep, err := port.Validate(context.Background(), userCaller)
	if err != nil || !rep.OK {
		t.Fatalf("validate = %+v err=%v (ok: a reload would publish)", rep, err)
	}
	if _, err := port.Validate(context.Background(), filesCaller); err == nil {
		t.Fatal("plugin settings.validate allowed")
	}
	// A failing Inputs seam never becomes a panic or a value leak.
	broken := Settings{Svc: svc, Inputs: staticInputs{err: context.DeadlineExceeded}}
	if _, err := broken.Validate(context.Background(), userCaller); err == nil {
		t.Fatal("broken inputs allowed")
	} else if code, reason := codedOf(t, err); code != "timeout" || reason != "input_deadline" {
		t.Fatalf("broken inputs: %s/%s", code, reason)
	}
}

func TestSettingsValidateReportsProblemsWithoutApplying(t *testing.T) {
	svc := newSet(settings.Options{})
	good := declaredSettingsInput(t, `{"plugins":{"files":{"tree":true}}}`)
	applyTo(t, svc, good)
	// Fresh input with a type violation: tree must be a bool. The dry run
	// answers ok:false with the problem, and the live snapshot keeps the
	// good value.
	bad := declaredSettingsInput(t, `{"plugins":{"files":{"tree":"yes"}}}`)
	port := Settings{Svc: svc, Inputs: staticInputs{in: bad}}
	if res, err := port.Validate(context.Background(), userCaller); err != nil || res.OK || len(res.Problems) != 1 || res.Problems[0].Code != "invalid_value" {
		t.Fatalf("type violation: %+v err=%v", res, err)
	}
	if v := svc.Snapshot().Bool("plugins.files.tree"); !v {
		t.Fatal("validate mutated the live snapshot")
	}
	// Good input validates clean and still applies nothing.
	port.Inputs = staticInputs{in: good}
	rep, err := port.Validate(context.Background(), userCaller)
	if err != nil || !rep.OK {
		t.Fatalf("validate = %+v err=%v", rep, err)
	}
	if v := svc.Snapshot().Bool("plugins.files.tree"); !v {
		t.Fatal("dry validate mutated the live snapshot")
	}
}

func TestSettingsSchemaEntriesAreDetached(t *testing.T) {
	svc := newSet(settings.Options{})
	in := declaredSettingsInput(t, `{}`)
	applyTo(t, svc, in)
	port := Settings{Svc: svc, Inputs: staticInputs{in: in}}
	entries, err := port.Schema(context.Background(), userCaller, "plugin:a")
	if err != nil || len(entries) != 1 {
		t.Fatalf("schema = %+v err=%v", entries, err)
	}
	entries[0].Key = "mutated"
	again, _ := port.Schema(context.Background(), userCaller, "plugin:a")
	if again[0].Key != "plugins.a.mode" {
		t.Fatal("Schema shared state with its caller")
	}
}
