package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestDefaultPingMissingSocket(t *testing.T) {
	dir := t.TempDir()
	err := DefaultPing(filepath.Join(dir, "absent.sock"), 100*time.Millisecond)
	if !errors.Is(err, ErrSocketMissing) {
		t.Fatalf("DefaultPing on a missing socket = %v, want ErrSocketMissing", err)
	}
	if got := pingVerdict(err); got != VerdictStale {
		t.Fatalf("verdict = %v, want stale", got)
	}
}

func TestDefaultPingNotASocket(t *testing.T) {
	// Dialling a regular file fails with a platform-dependent errno
	// (ENOTSOCK or ECONNREFUSED); both are stale.
	path := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := DefaultPing(path, 100*time.Millisecond)
	if err == nil {
		t.Fatal("dialling a regular file should fail")
	}
	if got := pingVerdict(err); got != VerdictStale {
		t.Fatalf("verdict = %v (%v), want stale", got, err)
	}
	if !errors.Is(err, ErrSocketRefused) && !errors.Is(err, ErrSocketMissing) {
		t.Fatalf("error should be a stale sentinel, got: %v", err)
	}
}

func TestDefaultPingTimeoutIsUnresponsive(t *testing.T) {
	// An unreachable path with no parent fails fast, but the timeout
	// wrapper on a write/read failure is classified unresponsive, never
	// stale: uncertainty keeps the entry.
	err := fmt.Errorf("%w: no reply", ErrPingTimeout)
	if got := pingVerdict(err); got != VerdictUnresponsive {
		t.Fatalf("verdict = %v, want unresponsive", got)
	}
	if !strings.Contains(err.Error(), "no reply") {
		t.Fatalf("sentinel should keep the cause text, got: %v", err)
	}
}
