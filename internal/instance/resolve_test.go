package instance

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeNet is the injected Ping for resolver tests: a map of sockets that
// answer, a map that accepts but never replies, everything else refused.
// No real socket is involved anywhere.
type fakeNet struct {
	live         map[string]bool
	unresponsive map[string]bool
}

func newFakeNet() *fakeNet {
	return &fakeNet{live: map[string]bool{}, unresponsive: map[string]bool{}}
}

func (f *fakeNet) deps() *Deps {
	return &Deps{Ping: func(p string) error {
		switch {
		case f.live[p]:
			return nil
		case f.unresponsive[p]:
			return fmt.Errorf("%w: slow", ErrPingTimeout)
		default:
			return fmt.Errorf("%w: dead", ErrSocketRefused)
		}
	}}
}

// fixture builds a runtime directory from the entries the closure builds
// for it, plus a regular file standing in for each socket. All of an
// entry's paths are derived from the fixture's own directory.
func fixture(t *testing.T, build func(dir string) []Entry) (string, *fakeNet) {
	t.Helper()
	dir := t.TempDir()
	net := newFakeNet()
	for _, e := range build(dir) {
		if err := writeEntryFile(dir, e); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(SocketPath(dir, e.ID), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		net.live[e.Socket] = true
	}
	return dir, net
}

func pidEntry(dir, id string, pid int) Entry {
	return Entry{ID: id, PID: pid, Socket: SocketPath(dir, id), StartedAt: startedAt, ProtocolVersion: "2.0"}
}

func namedEntry(dir, name string, pid int) Entry {
	e := pidEntry(dir, name, pid)
	e.Name = name
	return e
}

func TestResolveSocketFlag(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry { return nil })
	got, err := Resolve(ResolveOptions{Dir: dir, Socket: "/explicit.sock", Deps: net.deps()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourceSocketFlag || got.Socket != "/explicit.sock" {
		t.Fatalf("got %+v, want the explicit socket via flag", got)
	}
	if got.Entry != nil {
		t.Fatalf("entry = %+v, want nil: a bare socket path carries no registration", got.Entry)
	}
}

func TestResolveOrderFlagBeatsEnvBeatsDiscovery(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	cases := []struct {
		label  string
		opts   ResolveOptions
		source Source
		socket string
	}{
		{
			label:  "instance flag beats env socket path",
			opts:   ResolveOptions{Instance: "notes", EnvSocketPath: "/env.sock"},
			source: SourceFlag,
			socket: SocketPath(dir, "notes"),
		},
		{
			label:  "env socket path beats env instance",
			opts:   ResolveOptions{EnvSocketPath: "/env.sock", EnvInstance: "notes"},
			source: SourceEnv,
			socket: "/env.sock",
		},
		{
			label:  "env socket path alone",
			opts:   ResolveOptions{EnvSocketPath: "/env.sock"},
			source: SourceEnv,
			socket: "/env.sock",
		},
		{
			label:  "env instance beats discovery",
			opts:   ResolveOptions{EnvInstance: "4242"},
			source: SourceEnv,
			socket: SocketPath(dir, "4242"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			opts := tc.opts
			opts.Dir = dir
			opts.Deps = net.deps()
			got, err := Resolve(opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Source != tc.source {
				t.Fatalf("source = %v, want %v", got.Source, tc.source)
			}
			if got.Socket != tc.socket {
				t.Fatalf("socket = %q, want %q", got.Socket, tc.socket)
			}
		})
	}
}

func TestResolveConflictingFlags(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	_, err := Resolve(ResolveOptions{Dir: dir, Socket: "/s", Instance: "notes", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindUsage {
		t.Fatalf("error = %v, want KindUsage", err)
	}
	if want := "--socket and --instance are mutually exclusive"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestResolveMissingDirIsUsage(t *testing.T) {
	_, err := Resolve(ResolveOptions{})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindUsage {
		t.Fatalf("error = %v, want KindUsage", err)
	}
}

func TestResolveInstanceFlag(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	got, err := Resolve(ResolveOptions{Dir: dir, Instance: "notes", Deps: net.deps()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourceFlag || got.Socket != SocketPath(dir, "notes") {
		t.Fatalf("got %+v, want the notes socket via flag", got)
	}
	if got.Entry == nil || got.Entry.PID != 5120 || got.Entry.Name != "notes" {
		t.Fatalf("entry = %+v, want the notes entry", got.Entry)
	}
}

func TestResolveInstanceFlagByPidID(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{pidEntry(dir, "4242", 4242)}
	})
	got, err := Resolve(ResolveOptions{Dir: dir, Instance: "4242", Deps: net.deps()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Entry == nil || got.Entry.PID != 4242 || got.Entry.Name != "" {
		t.Fatalf("entry = %+v, want the pid entry without a name", got.Entry)
	}
}

func TestResolveInstanceFlagInvalidIDIsUsage(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry { return nil })
	_, err := Resolve(ResolveOptions{Dir: dir, Instance: "Foo_1", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindUsage {
		t.Fatalf("error = %v, want KindUsage", err)
	}
}

func TestResolveInstanceFlagNotRegistered(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry { return nil })
	_, err := Resolve(ResolveOptions{Dir: dir, Instance: "ghost", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
	if want := `instance "ghost" is not running`; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestResolveInstanceFlagStaleRemovesEntry(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	net.live = map[string]bool{} // nothing answers: the entry is stale
	_, err := Resolve(ResolveOptions{Dir: dir, Instance: "notes", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
	if want := `instance "notes" is not running (stale entry removed)`; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if _, serr := os.Lstat(EntryPath(dir, "notes")); serr == nil {
		t.Error("stale entry should have been removed")
	}
	if _, serr := os.Lstat(SocketPath(dir, "notes")); serr == nil {
		t.Error("stale socket should have been removed")
	}
}

func TestResolveInstanceFlagUnresponsiveIsTransport(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	net.unresponsive[SocketPath(dir, "notes")] = true
	net.live[SocketPath(dir, "notes")] = false
	_, err := Resolve(ResolveOptions{Dir: dir, Instance: "notes", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindTransport {
		t.Fatalf("error = %v, want KindTransport", err)
	}
	// An unresponsive instance keeps its registration.
	if _, serr := os.Lstat(EntryPath(dir, "notes")); serr != nil {
		t.Error("unresponsive entry must be kept")
	}
}

func TestResolveEnvInstance(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{pidEntry(dir, "4242", 4242)}
	})
	got, err := Resolve(ResolveOptions{Dir: dir, EnvInstance: "4242", Deps: net.deps()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourceEnv || got.Socket != SocketPath(dir, "4242") {
		t.Fatalf("got %+v, want 4242 via env", got)
	}
}

func TestResolveEnvInstanceInvalidIsUsage(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry { return nil })
	_, err := Resolve(ResolveOptions{Dir: dir, EnvInstance: "no such", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindUsage {
		t.Fatalf("error = %v, want KindUsage", err)
	}
}

func TestResolveEnvInstanceStaleDoesNotFallThrough(t *testing.T) {
	// The spec's no-fallback rule: the env step applies and fails, so a
	// live instance elsewhere in the directory is never a silent fallback.
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	delete(net.live, SocketPath(dir, "4242"))
	_, err := Resolve(ResolveOptions{Dir: dir, EnvInstance: "4242", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
}

func TestResolveDiscoveryZero(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry { return nil })
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
	if want := "no instance is running"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestResolveDiscoveryOnlyStale(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	net.live = map[string]bool{}
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
	if _, serr := os.Lstat(EntryPath(dir, "notes")); serr == nil {
		t.Error("stale entry should have been removed by discovery")
	}
}

func TestResolveDiscoveryOneLiveAmongStale(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	delete(net.live, SocketPath(dir, "4242"))
	got, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourceDiscovery || got.Socket != SocketPath(dir, "notes") {
		t.Fatalf("got %+v, want notes via discovery", got)
	}
	if got.Entry == nil || got.Entry.PID != 5120 {
		t.Fatalf("entry = %+v, want the notes entry", got.Entry)
	}
	if _, serr := os.Lstat(EntryPath(dir, "4242")); serr == nil {
		t.Error("stale entry should have been removed")
	}
}

func TestResolveDiscoveryTwoLiveIsAmbiguous(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindAmbiguous {
		t.Fatalf("error = %v, want KindAmbiguous", err)
	}
	if len(e.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want both live instances", e.Candidates)
	}
	byID := map[string]Status{}
	for _, c := range e.Candidates {
		byID[c.Entry.ID] = c
	}
	if c := byID["notes"]; c.Entry.PID != 5120 || c.Entry.Socket != SocketPath(dir, "notes") {
		t.Fatalf("notes candidate = %+v, want its pid and socket path", c)
	}
	if c := byID["4242"]; c.Entry.PID != 4242 || c.Entry.Socket != SocketPath(dir, "4242") {
		t.Fatalf("4242 candidate = %+v, want its pid and socket path", c)
	}
	if want := "2 instances are running; choose one with --instance <id>"; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("message = %q, want prefix %q", err.Error(), want)
	}
}

func TestResolveDiscoveryOneLiveOneUnresponsive(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	net.unresponsive[SocketPath(dir, "4242")] = true
	net.live[SocketPath(dir, "4242")] = false
	got, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourceDiscovery || got.Entry == nil || got.Entry.PID != 5120 {
		t.Fatalf("got %+v, want notes; unresponsive entries are not candidates", got)
	}
	if _, serr := os.Lstat(EntryPath(dir, "4242")); serr != nil {
		t.Error("unresponsive entry must be kept")
	}
}

func TestResolveDiscoveryOnlyUnresponsive(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	net.unresponsive[SocketPath(dir, "notes")] = true
	net.live[SocketPath(dir, "notes")] = false
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
}

func TestErrorAmbiguousRendering(t *testing.T) {
	e := &Error{
		Kind:       KindAmbiguous,
		Message:    "2 instances are running; choose one with --instance <id>",
		Candidates: []Status{},
	}
	e.Candidates = append(e.Candidates,
		Status{Entry: Entry{ID: "notes", PID: 5120, StartedAt: startedAt}, Verdict: VerdictLive},
		Status{Entry: Entry{ID: "4242", PID: 4242, StartedAt: startedAt.Add(2*time.Minute + 11*time.Second)}, Verdict: VerdictLive},
	)
	want := "2 instances are running; choose one with --instance <id>\n" +
		"  4242   started 2026-10-22T06:02:11Z\n" +
		"  notes  started 2026-10-22T06:00:00Z"
	if got := e.Error(); got != want {
		t.Fatalf("Error() =\n%s\nwant\n%s", got, want)
	}
}

func TestErrorKindExitCode(t *testing.T) {
	cases := map[ErrorKind]int{
		KindUsage:      2,
		KindNoInstance: 3,
		KindAmbiguous:  4,
		KindTransport:  5,
		KindTaken:      1,
	}
	for k, want := range cases {
		if got := k.ExitCode(); got != want {
			t.Fatalf("%v.ExitCode() = %d, want %d", k, got, want)
		}
	}
}

func TestSourceString(t *testing.T) {
	cases := map[Source]string{
		SourceFlag:       "flag",
		SourceSocketFlag: "socket-flag",
		SourceEnv:        "env",
		SourceDiscovery:  "discovery",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("Source(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}
