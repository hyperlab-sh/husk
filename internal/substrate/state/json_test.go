package state_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func assertJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestMergePatchRFC7396(t *testing.T) {
	// Object-result examples from RFC 7396 Appendix A, plus precision and
	// empty-target cases. Non-object root results are refused by State.
	for _, tc := range []struct{ target, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
		{`{"a":null}`, `{"a":{"b":null}}`, `{"a":{}}`},
		{`{"n":9007199254740993}`, `{"x":1}`, `{"n":9007199254740993,"x":1}`},
	} {
		t.Run(tc.target+"/"+tc.patch, func(t *testing.T) {
			s := state.New(state.Options{})
			spec := baseSpec("p.state", "p")
			spec.Initial = []byte(tc.target)
			h := mustRegister(t, s, spec)
			if _, err := h.Patch([]byte(tc.patch), 1); err != nil {
				t.Fatal(err)
			}
			got := s.Snapshot().Slices[h.Namespace()].Value
			assertJSON(t, got, []byte(tc.want))
			if tc.want == `{"n":9007199254740993,"x":1}` && string(got) != tc.want {
				t.Fatal("lost numeric precision", string(got))
			}
		})
	}
}

func TestPatchFailuresAndCAS(t *testing.T) {
	s := state.New(state.Options{})
	spec := baseSpec("p.state", "p")
	spec.MaxBytes = 20
	h := mustRegister(t, s, spec)
	if rev, err := h.Patch([]byte(`{"n":1}`), 1); err != nil || rev != 2 {
		t.Fatalf("CAS: %d %v", rev, err)
	}
	before := s.Snapshot()
	for _, tc := range []struct {
		patch    string
		expected uint64
		err      error
	}{
		{`{"n":2}`, 1, state.ErrConflict},
		{`{"n":1}`, 1, state.ErrConflict}, // Even a no-op checks the CAS.
		{"{", 0, state.ErrInvalidValue},
		{"", 0, state.ErrInvalidValue},
		{"null", 0, state.ErrInvalidValue},
		{"[]", 0, state.ErrInvalidValue},
		{`"scalar"`, 0, state.ErrInvalidValue},
		{`{"oversized":"0123456789"}`, 0, state.ErrTooLarge},
	} {
		if rev, err := h.Patch([]byte(tc.patch), tc.expected); !errors.Is(err, tc.err) || rev != 2 {
			t.Fatalf("Patch(%s,%d) = %d, %v", tc.patch, tc.expected, rev, err)
		}
		if s.Snapshot() != before {
			t.Fatal("rejected patch changed snapshot")
		}
	}
	if rev, err := h.Patch([]byte(`{}`), 2); err != nil || rev != 2 || s.Snapshot() != before {
		t.Fatal("empty patch should not commit")
	}
	if rev, err := h.Patch([]byte(`{"n":2}`), 0); err != nil || rev != 3 {
		t.Fatal("unconditional patch failed", rev, err)
	}
}

func TestJSONPointersRFC6901(t *testing.T) {
	s := state.New(state.Options{})
	spec := baseSpec("p.state", "p")
	spec.Initial = []byte(`{"foo":["bar","baz"],"":0,"a/b":1,"c%d":2,"e^f":3,"g|h":4,"i\\j":5,"k\"l":6," ":7,"m~n":8,"nil":null,"~1":{"nested":9007199254740993}}`)
	h := mustRegister(t, s, spec)
	entry := s.Snapshot().Slices[h.Namespace()]
	for _, tc := range []struct{ pointer, want string }{
		{"", string(spec.Initial)}, {"/foo", `["bar","baz"]`}, {"/foo/0", `"bar"`}, {"/", "0"},
		{"/a~1b", "1"}, {"/c%d", "2"}, {"/e^f", "3"}, {"/g|h", "4"}, {"/i\\j", "5"},
		{"/k\"l", "6"}, {"/ ", "7"}, {"/m~0n", "8"}, {"/nil", "null"}, {"/~01/nested", "9007199254740993"},
	} {
		t.Run(tc.pointer, func(t *testing.T) {
			got, ok := entry.At(tc.pointer)
			if !ok || string(got) != tc.want {
				t.Fatalf("At(%q) = %s, %v; want %s", tc.pointer, got, ok, tc.want)
			}
			if len(got) > 0 {
				got[0] = '!'
			}
			if !json.Valid(entry.Value) {
				t.Fatal("pointer result aliases snapshot")
			}
		})
	}
	for _, pointer := range []string{"foo", "#/foo", "/missing", "/foo/-", "/foo/2", "/foo/01", "/foo/+1", "/foo/-1", "/foo/999999999999999999999999999", "/foo/", "/foo/0/member", "/nil/x", "/a~2b", "/m~", "/%61"} {
		if got, ok := entry.At(pointer); ok {
			t.Fatalf("bad pointer %q resolved: %s", pointer, got)
		}
	}
}
