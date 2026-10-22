package instance

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestClassifyVerdicts(t *testing.T) {
	cases := []struct {
		label string
		d     *Deps
		want  Verdict
	}{
		{"ping replies", &Deps{Ping: func(string) error { return nil }}, VerdictLive},
		{"missing sentinel", &Deps{Ping: func(string) error { return fmt.Errorf("%w: gone", ErrSocketMissing) }}, VerdictStale},
		{"refused sentinel", &Deps{Ping: func(string) error { return fmt.Errorf("%w: dead", ErrSocketRefused) }}, VerdictStale},
		{"raw ENOENT", &Deps{Ping: func(string) error { return syscall.ENOENT }}, VerdictStale},
		{"raw ECONNREFUSED", &Deps{Ping: func(string) error { return syscall.ECONNREFUSED }}, VerdictStale},
		{"timeout sentinel", &Deps{Ping: func(string) error { return fmt.Errorf("%w: slow", ErrPingTimeout) }}, VerdictUnresponsive},
		{"unknown error", &Deps{Ping: func(string) error { return errors.New("boom") }}, VerdictUnresponsive},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			e := Entry{ID: "notes", PID: 7, Socket: "/unused"}
			if got := tc.d.classify(e); got != tc.want {
				t.Fatalf("classify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVerdictString(t *testing.T) {
	cases := map[Verdict]string{
		VerdictLive:         "live",
		VerdictStale:        "stale",
		VerdictUnresponsive: "unresponsive",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Fatalf("Verdict(%d).String() = %q, want %q", int(v), got, want)
		}
	}
}

func TestSurveyRemovesStaleKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	live := Entry{ID: "a", PID: 1, Socket: SocketPath(dir, "a"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	stale := Entry{ID: "b", PID: 2, Socket: SocketPath(dir, "b"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	slow := Entry{ID: "c", PID: 3, Socket: SocketPath(dir, "c"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	for _, e := range []Entry{live, stale, slow} {
		if err := writeEntryFile(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	// A stale socket leaves its file behind; a regular file stands in.
	if err := os.WriteFile(SocketPath(dir, "b"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Deps{Ping: func(p string) error {
		switch p {
		case live.Socket:
			return nil
		case slow.Socket:
			return fmt.Errorf("%w: slow", ErrPingTimeout)
		default:
			return fmt.Errorf("%w: dead", ErrSocketRefused)
		}
	}}

	got, err := Survey(dir, d)
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Survey = %+v, want the live and the unresponsive entry", got)
	}
	if got[0].Entry.ID != "a" || got[0].Verdict != VerdictLive {
		t.Fatalf("first row = %+v, want live a", got[0])
	}
	if got[1].Entry.ID != "c" || got[1].Verdict != VerdictUnresponsive {
		t.Fatalf("second row = %+v, want unresponsive c", got[1])
	}
	if _, err := os.Lstat(EntryPath(dir, "b")); err == nil {
		t.Error("stale entry should have been removed")
	}
	if _, err := os.Lstat(SocketPath(dir, "b")); err != nil {
		t.Error("stale cleanup must leave the socket alone")
	}
	if _, err := os.Lstat(EntryPath(dir, "c")); err != nil {
		t.Error("unresponsive entry must be kept")
	}
}

func TestSurveyEmptyAndMissingDir(t *testing.T) {
	got, err := Survey(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Survey = %+v, want empty", got)
	}
	got, err = Survey(t.TempDir()+"/nowhere", nil)
	if err != nil {
		t.Fatalf("Survey on a missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Survey = %+v, want empty", got)
	}
}

// DefaultPing itself is deliberately not tested: every test would need a
// real dial, and DefaultPingTimeoutIsUnresponsive only reached the
// filesystem or hung for its full budget. Its classification logic is
// pinned by TestClassifyVerdicts (missing/refused sentinels, raw ENOENT
// and ECONNREFUSED, timeout sentinel, unknown error), and its dial layer
// is the stdlib's.
