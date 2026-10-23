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
	// The bare socket paths the order tests name (--socket,
	// HUSK_SOCKET_PATH) answer by default; the dead/unresponsive cases
	// delete or redirect them.
	net.live["/explicit.sock"] = true
	net.live["/env.sock"] = true
	return dir, net
}

func pidEntry(dir, id string, pid int) Entry {
	return Entry{ID: id, PID: pid, Socket: SocketPath(dir, id), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
}

func namedEntry(dir, name string, pid int) Entry {
	return pidEntry(dir, name, pid)
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

// neverPing fails the test if a probe is attempted: explicit targets are
// checked by the command's own connection, not by Resolve.
func neverPing(t *testing.T) *Deps {
	return &Deps{Ping: func(p string) error {
		t.Errorf("Resolve probed %s; explicit targets must not be probed", p)
		return nil
	}}
}

func TestResolveExplicitTargetsAreNotProbed(t *testing.T) {
	dir, _ := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	cases := []struct {
		name string
		opts ResolveOptions
		want string
	}{
		{"socket flag, dead path", ResolveOptions{Socket: "/absent.sock"}, "/absent.sock"},
		{"HUSK_SOCKET_PATH, dead path", ResolveOptions{EnvSocketPath: "/absent.sock"}, "/absent.sock"},
		{"instance flag", ResolveOptions{Instance: "notes"}, SocketPath(dir, "notes")},
		{"HUSK_INSTANCE", ResolveOptions{EnvInstance: "notes"}, SocketPath(dir, "notes")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Dir = dir
			opts.Deps = neverPing(t)
			got, err := Resolve(opts)
			if err != nil || got.Socket != tc.want {
				t.Fatalf("got %+v, %v; want socket %q", got, err, tc.want)
			}
		})
	}
	if _, err := os.Lstat(EntryPath(dir, "notes")); err != nil {
		t.Errorf("Resolve must not touch the entry: %v", err)
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
	if got.Entry == nil || got.Entry.PID != 5120 {
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
	if got.Entry == nil || got.Entry.PID != 4242 {
		t.Fatalf("entry = %+v, want the pid entry", got.Entry)
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

func TestResolveEnvInstanceNotRegisteredDoesNotFallThrough(t *testing.T) {
	// The spec's no-fallback rule: the env step applies and fails, so a
	// live instance elsewhere in the directory is never a silent fallback.
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	_, err := Resolve(ResolveOptions{Dir: dir, EnvInstance: "4242", Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
}

func TestResolveAbsentDirectory(t *testing.T) {
	cases := []struct {
		name string
		opts ResolveOptions
		kind ErrorKind
		msg  string
	}{
		{"discovery", ResolveOptions{}, KindNoInstance, "no instance is running"},
		{"instance flag", ResolveOptions{Instance: "notes"}, KindNoInstance, `instance "notes" is not running`},
		{"HUSK_INSTANCE", ResolveOptions{EnvInstance: "notes"}, KindNoInstance, `instance "notes" is not running`},
		{"invalid id is still usage", ResolveOptions{Instance: "Foo_1"}, KindUsage, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.DirAbsent = true
			opts.Deps = neverPing(t)
			_, err := Resolve(opts)
			var e *Error
			if !errors.As(err, &e) || e.Kind != tc.kind || (tc.msg != "" && err.Error() != tc.msg) {
				t.Fatalf("error = %v, want kind %v %q", err, tc.kind, tc.msg)
			}
		})
	}
	t.Run("DirAbsent wins over a populated Dir: the registry is empty", func(t *testing.T) {
		dir, net := fixture(t, func(dir string) []Entry { return []Entry{namedEntry(dir, "notes", 5120)} })
		_, err := Resolve(ResolveOptions{Dir: dir, DirAbsent: true, Deps: net.deps()})
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindNoInstance {
			t.Fatalf("error = %v, want KindNoInstance", err)
		}
	})
	t.Run("a socket path needs no directory", func(t *testing.T) {
		got, err := Resolve(ResolveOptions{DirAbsent: true, Socket: "/s.sock"})
		if err != nil || got.Socket != "/s.sock" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
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
	replaceWithDeadSocket(t, dir, "notes")
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindNoInstance {
		t.Fatalf("error = %v, want KindNoInstance", err)
	}
	if _, serr := os.Lstat(EntryPath(dir, "notes")); serr == nil {
		t.Error("stale entry should have been removed by discovery")
	}
	if _, serr := os.Lstat(SocketPath(dir, "notes")); serr == nil {
		t.Error("the refused socket should have been unlinked")
	}
}

func TestResolveDiscoveryOneLiveAmongStale(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	delete(net.live, SocketPath(dir, "4242"))
	replaceWithDeadSocket(t, dir, "4242")
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
	if _, serr := os.Lstat(SocketPath(dir, "4242")); serr == nil {
		t.Error("the refused socket should have been unlinked")
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
	if len(got.Skipped) != 1 || got.Skipped[0].Entry.ID != "4242" || got.Skipped[0].Verdict != VerdictUnresponsive {
		t.Errorf("Skipped = %+v, want the unresponsive 4242", got.Skipped)
	}
}

func TestResolveDiscoveryOnlyUnresponsiveIsTransport(t *testing.T) {
	// Exit 5, not 3: a script that starts an instance on "none running"
	// must not start one beside a hung one.
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120), pidEntry(dir, "4242", 4242)}
	})
	for _, id := range []string{"notes", "4242"} {
		net.unresponsive[SocketPath(dir, id)] = true
		net.live[SocketPath(dir, id)] = false
	}
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindTransport {
		t.Fatalf("error = %v, want KindTransport", err)
	}
	if want := "2 instances are registered but none answered within 500ms: 4242, notes"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if len(e.Skipped) != 2 {
		t.Errorf("Skipped = %+v, want both", e.Skipped)
	}
	for _, id := range []string{"notes", "4242"} {
		if _, serr := os.Lstat(EntryPath(dir, id)); serr != nil {
			t.Errorf("unresponsive entry %s must be kept", id)
		}
	}
}

func TestResolveDiscoveryOneUnresponsiveMessageIsSingular(t *testing.T) {
	dir, net := fixture(t, func(dir string) []Entry {
		return []Entry{namedEntry(dir, "notes", 5120)}
	})
	net.unresponsive[SocketPath(dir, "notes")] = true
	net.live[SocketPath(dir, "notes")] = false
	_, err := Resolve(ResolveOptions{Dir: dir, Deps: net.deps()})
	if want := "1 instance is registered but none answered within 500ms: notes"; err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestErrorAmbiguousRendering(t *testing.T) {
	e := &Error{
		Kind:       KindAmbiguous,
		Message:    "2 instances are running; choose one with --instance <id>",
		Candidates: []Status{},
	}
	e.Candidates = append(e.Candidates,
		Status{Entry: Entry{ID: "notes", PID: 5120, StartedAt: fixedTime()}, Verdict: VerdictLive},
		Status{Entry: Entry{ID: "4242", PID: 4242, StartedAt: fixedTime().Add(2*time.Minute + 11*time.Second)}, Verdict: VerdictLive},
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

// replaceWithDeadSocket swaps the fixture's placeholder file for a real
// socket nobody listens on: what a crash leaves behind.
func replaceWithDeadSocket(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.Remove(SocketPath(dir, id)); err != nil {
		t.Fatal(err)
	}
	deadSocket(t, dir, id)
}
