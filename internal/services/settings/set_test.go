package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type writeProbe struct {
	calls int
	doc   store.Doc
	err   error
}

func (w *writeProbe) Patch(name store.DocName, layer store.Layer, profile string, patch store.Doc) error {
	if name != "config" || layer != store.LayerUser || profile != "" {
		panic("wrong write target")
	}
	w.calls++
	w.doc = patch
	return w.err
}

type reloadProbe struct{ calls int }

func (r *reloadProbe) AwaitSettings() Waiter { r.calls++; return r }
func (*reloadProbe) Wait(context.Context) ([]c.Report, error) {
	return []c.Report{{Service: "settings", Gen: 9, Applied: true}}, nil
}
func writeService(t *testing.T, schema ...Entry) (*Service, *writeProbe, *reloadProbe) {
	t.Helper()
	w, r := &writeProbe{}, &reloadProbe{}
	s, err := New(Options{Schema: schema, Store: w, Post: func(f func()) bool { f(); return true }})
	if err != nil {
		t.Fatal(err)
	}
	s.Bind(r)
	return s, w, r
}
func setError(t *testing.T, err error, code, reason string) *c.Error {
	t.Helper()
	var e *c.Error
	if !errors.As(err, &e) || e.Code != code || e.Reason != reason {
		t.Fatalf("error %v, want %s/%s", err, code, reason)
	}
	return e
}
func TestSettingsSetPluginDeniedBeforeStore(t *testing.T) {
	// No snapshot, writer or reloader: authorization must precede all reads.
	s := &Service{}
	for _, caller := range []c.Caller{{Kind: protocol.CallerPlugin, PluginID: "unit"}, {Kind: protocol.CallerPlugin}} {
		_, _, err := s.Set(context.Background(), caller, "theme.name", nil)
		setError(t, err, "denied", "user_only")
	}
	for _, caller := range []c.Caller{{}, {Kind: "other"}, {Kind: protocol.CallerInput}, {Kind: protocol.CallerCLI, PluginID: "unit"}} {
		_, _, err := s.Set(context.Background(), caller, "theme.name", nil)
		setError(t, err, "denied", "invalid_caller")
	}
}
func TestSettingsSetUnknownKeyWritesNothing(t *testing.T) {
	s, w, r := writeService(t)
	_, _, err := s.Set(context.Background(), c.Caller{Kind: "cli"}, "nope.key", true)
	setError(t, err, "not_found", "unknown_setting")
	if w.calls != 0 || r.calls != 0 {
		t.Fatal("unknown key wrote or reloaded")
	}
}
func TestSettingsSetInvalidValueWritesNothingAndNamesKey(t *testing.T) {
	bound := int64(3)
	for _, tc := range []struct {
		entry Entry
		value any
	}{
		{Entry{Key: "dist.n", Type: TypeInt, Default: int64(1), Max: &bound}, 4},
		{Entry{Key: "dist.n", Type: TypeInt, Default: int64(3), Min: &bound}, 2},
		{Entry{Key: "dist.n", Type: TypeInt, Default: int64(1)}, 1.5},
		{Entry{Key: "dist.n", Type: TypeInt, Default: int64(1)}, json.Number("9223372036854775807.1")},
		{Entry{Key: "dist.n", Type: TypeInt, Default: int64(1)}, json.Number("9223372036854775808.0")},
		{Entry{Key: "dist.n", Type: TypeDuration, Default: int64(1)}, "1ms"},
		{Entry{Key: "dist.n", Type: TypeEnum, Default: "yes", Enum: []string{"yes"}}, "no"},
		{Entry{Key: "dist.n", Type: TypeStrings, Default: []string{}}, []any{1}},
		{Entry{Key: "dist.n", Type: TypeBool, Default: false}, "true"},
		{Entry{Key: "dist.n", Type: TypeString, Default: ""}, 42},
	} {
		t.Run(tc.entry.Type+"_"+tc.entry.Key, func(t *testing.T) {
			tc.entry.Owner = "dist"
			s, w, r := writeService(t, tc.entry)
			_, _, err := s.Set(context.Background(), c.Caller{Kind: "cli"}, tc.entry.Key, tc.value)
			e := setError(t, err, "invalid_params", "invalid_value")
			if e.Key != tc.entry.Key || w.calls != 0 || r.calls != 0 {
				t.Fatalf("%+v writes=%d reloads=%d", e, w.calls, r.calls)
			}
		})
	}
}
func TestSettingsSetNullRefused(t *testing.T) {
	s, w, r := writeService(t)
	for _, value := range []any{nil, []string(nil), []any(nil), map[string]any(nil)} {
		_, _, err := s.Set(context.Background(), c.Caller{Kind: "cli"}, "husk.plugins.paths", value)
		setError(t, err, "invalid_params", "null_value")
	}
	if w.calls != 0 || r.calls != 0 {
		t.Fatal("null wrote or reloaded")
	}
}
func TestSettingsSetAcceptsExactIntegralJSONNumbers(t *testing.T) {
	for _, value := range []json.Number{"1.0", "1e3", "9223372036854775807.0", "-9223372036854775808e0"} {
		t.Run(string(value), func(t *testing.T) {
			s, w, _ := writeService(t, Entry{Key: "dist.n", Owner: "dist", Type: TypeInt, Default: int64(0)})
			if _, _, err := s.Set(context.Background(), c.Caller{Kind: protocol.CallerCLI}, "dist.n", value); err != nil {
				t.Fatal(err)
			}
			if w.calls != 1 {
				t.Fatal("valid integer was not persisted")
			}
		})
	}
}

func TestSettingsSetStoreFailureNoReload(t *testing.T) {
	s, w, r := writeService(t)
	w.err = errors.New("cannot patch existing document")
	_, _, err := s.Set(context.Background(), c.Caller{Kind: "cli"}, "theme.name", "evening")
	e := setError(t, err, "unavailable", "store_error")
	if e.Message != w.err.Error() || w.calls != 1 || r.calls != 0 {
		t.Fatalf("%+v writes=%d reloads=%d", e, w.calls, r.calls)
	}
}
func TestSettingsSetHostWritesNestedKeyAndWaits(t *testing.T) {
	s, w, r := writeService(t, Entry{Key: "dist.part.key", Owner: "dist", Type: TypeBool, Default: false})
	source, reports, err := s.Set(context.Background(), c.Caller{Kind: "host"}, "dist.part.key", true)
	want := store.Doc{"dist": store.Doc{"part": store.Doc{"key": true}}}
	if err != nil || source != "default" || !reflect.DeepEqual(w.doc, want) || w.calls != 1 || r.calls != 1 || len(reports) != 1 || reports[0].Gen != 9 {
		t.Fatalf("%s %+v %v doc=%+v", source, reports, err, w.doc)
	}
}
