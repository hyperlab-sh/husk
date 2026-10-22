package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidName(t *testing.T) {
	cases := []struct {
		label   string
		in      string
		wantErr bool
	}{
		{"plain", "notes", false},
		{"single letter", "a", false},
		{"leading digit not all digits", "1a", false},
		{"digits and letters", "a1", false},
		{"inner hyphen", "a-1", false},
		{"double hyphen", "a--b", false},
		{"max length", strings.Repeat("a", MaxNameBytes), false},
		{"empty", "", true},
		{"all digits", "4242", true},
		{"all digits with leading zero", "04242", true},
		{"leading hyphen", "-abc", true},
		{"uppercase", "Foo_1", true},
		{"underscore", "foo_bar", true},
		{"space", "has space", true},
		{"dot", "a.b", true},
		{"non-ascii", "café", true},
		{"too long", strings.Repeat("a", MaxNameBytes+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			err := ValidName(tc.in)
			if got := err != nil; got != tc.wantErr {
				t.Fatalf("ValidName(%q) error = %v, want error: %v", tc.in, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), `[a-z0-9][a-z0-9-]*`) && !strings.Contains(err.Error(), "digits") {
				t.Fatalf("error should explain the rule, got: %v", err)
			}
		})
	}
}

func TestValidNameAllDigitsMessage(t *testing.T) {
	err := ValidName("4242")
	if err == nil {
		t.Fatal("all-digits name must be rejected")
	}
	if !strings.Contains(err.Error(), "pid") {
		t.Fatalf("all-digits error should mention the pid collision, got: %v", err)
	}
}

func TestValidID(t *testing.T) {
	cases := []struct {
		label   string
		in      string
		wantErr bool
	}{
		{"pid id", "4242", false},
		{"pid id zero", "0", false},
		{"name id", "notes", false},
		{"empty", "", true},
		{"invalid name", "Foo_1", true},
		{"pid id too long", strings.Repeat("9", MaxNameBytes+1), true},
		{"digits with separator", "42 42", true},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			err := ValidID(tc.in)
			if got := err != nil; got != tc.wantErr {
				t.Fatalf("ValidID(%q) error = %v, want error: %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestPaths(t *testing.T) {
	if got, want := SocketPath("/run/user/1000/husk", "notes"), "/run/user/1000/husk/notes.sock"; got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
	if got, want := EntryPath("/run/user/1000/husk", "4242"), "/run/user/1000/husk/4242.json"; got != want {
		t.Fatalf("EntryPath = %q, want %q", got, want)
	}
}

func TestCheckSocketPath(t *testing.T) {
	if err := CheckSocketPath(strings.Repeat("a", MaxSocketPathBytes)); err != nil {
		t.Fatalf("path of exactly %d bytes should fit: %v", MaxSocketPathBytes, err)
	}
	err := CheckSocketPath(strings.Repeat("a", MaxSocketPathBytes+1))
	if err == nil {
		t.Fatal("over-length path should be refused")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d bytes", MaxSocketPathBytes+1)) || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error should name the length and the limit, got: %v", err)
	}
}

func TestRuntimeDirFallbackToTemp(t *testing.T) {
	base := t.TempDir()
	dir, err := runtimeDir("", base, os.Getuid())
	if err != nil {
		t.Fatalf("runtimeDir: %v", err)
	}
	if want := filepath.Join(base, fmt.Sprintf("husk-%d", os.Getuid())); dir != want {
		t.Fatalf("runtimeDir = %q, want %q", dir, want)
	}
	assertPrivateDir(t, dir)
}

func TestRuntimeDirUnderXDG(t *testing.T) {
	base := t.TempDir()
	dir, err := runtimeDir(base, t.TempDir(), os.Getuid())
	if err != nil {
		t.Fatalf("runtimeDir: %v", err)
	}
	if want := base + "/husk"; dir != want {
		t.Fatalf("runtimeDir = %q, want %q", dir, want)
	}
	assertPrivateDir(t, dir)
}

func TestRuntimeDirReusesExistingSafeDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(base+"/husk", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+"/husk/marker", []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := runtimeDir(base, t.TempDir(), os.Getuid())
	if err != nil {
		t.Fatalf("runtimeDir: %v", err)
	}
	if dir != base+"/husk" {
		t.Fatalf("runtimeDir = %q, want the existing dir", dir)
	}
	data, err := os.ReadFile(base + "/husk/marker")
	if err != nil || string(data) != "marker" {
		t.Fatalf("existing dir was replaced or marker lost: %q, %v", data, err)
	}
}

func TestRuntimeDirCreatesMissingXDGParents(t *testing.T) {
	base := t.TempDir()
	xdg := base + "/missing/deeper"
	dir, err := runtimeDir(xdg, t.TempDir(), os.Getuid())
	if err != nil {
		t.Fatalf("runtimeDir: %v", err)
	}
	if dir != xdg+"/husk" {
		t.Fatalf("runtimeDir = %q, want %q", dir, xdg+"/husk")
	}
	assertPrivateDir(t, dir)
}

// TestRuntimeDirUnsafeXDGIsRefused: a set-but-unusable XDG_RUNTIME_DIR is
// fatal, never a silent drop to /tmp — the user pointed the session at a
// runtime root, and falling back would strand the instance where other
// sessions cannot see it.
func TestRuntimeDirUnsafeXDGIsRefused(t *testing.T) {
	cases := []struct {
		label string
		plant func(base string) error
	}{
		{"symlink", func(base string) error { return os.Symlink(base+"/target", base+"/husk") }},
		{"regular file", func(base string) error { return os.WriteFile(base+"/husk", []byte("x"), 0o600) }},
		{"group writable", func(base string) error { return os.MkdirAll(base+"/husk", 0o770) }},
		{"world readable", func(base string) error { return os.MkdirAll(base+"/husk", 0o755) }},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			base := t.TempDir()
			if err := tc.plant(base); err != nil {
				t.Fatal(err)
			}
			dir, err := runtimeDir(base, t.TempDir(), os.Getuid())
			if err == nil {
				t.Fatalf("runtimeDir = %q, want refusal", dir)
			}
			if !strings.Contains(err.Error(), "runtime dir") {
				t.Fatalf("error should name the directory, got: %v", err)
			}
		})
	}
}

// TestRuntimeDirRefusesUnsafeFallback: the /tmp/husk-<uid> candidate
// runs the same private-dir rule; a planted symlink is refused. The
// fallback root is injected, so the real /tmp is never touched.
func TestRuntimeDirRefusesUnsafeFallback(t *testing.T) {
	base := t.TempDir()
	fallback := filepath.Join(base, fmt.Sprintf("husk-%d", os.Getuid()))
	if err := os.Symlink("/tmp", fallback); err != nil {
		t.Fatal(err)
	}
	_, err := runtimeDir("", base, os.Getuid())
	if err == nil {
		t.Fatal("a planted fallback must be refused")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should say why, got: %v", err)
	}
}

// fixedTime is the fixed clock reading every fixture entry carries, so
// RemoveIfStale comparisons are exact.
func fixedTime() time.Time { return time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC) }

func TestEntryJSONShape(t *testing.T) {
	dir := runDir(t)
	// A zero StartedAt is stamped from the wall clock before writing, so
	// both fixtures set it explicitly and the shape test below checks the
	// JSON keys.
	pidEntry := Entry{
		ID:              "4242",
		PID:             4242,
		Socket:          SocketPath(dir, "4242"),
		StartedAt:       fixedTime(),
		ProtocolVersion: "2.0",
	}
	if err := Register(dir, pidEntry); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fields, err := readJSONFields(EntryPath(dir, "4242"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "pid", "socket", "started_at", "protocol_version"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("pid entry JSON lacks %q: %v", k, fields)
		}
	}
	for _, k := range []string{"name", "husk_version"} {
		if _, ok := fields[k]; ok {
			t.Errorf("pid entry JSON should omit %q: %v", k, fields)
		}
	}

	named := Entry{
		ID:              "notes",
		Name:            "notes",
		PID:             5120,
		Socket:          SocketPath(dir, "notes"),
		StartedAt:       fixedTime(),
		ProtocolVersion: "2.0",
		HuskVersion:     "0.9.0",
	}
	if err := Register(dir, named); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fields, err = readJSONFields(EntryPath(dir, "notes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "husk_version"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("named entry JSON lacks %q: %v", k, fields)
		}
	}
	if !reflect.DeepEqual(fields["name"], "notes") {
		t.Errorf("name field = %v, want notes", fields["name"])
	}
}

func TestRegisterEntryModeAndNoTempLeftovers(t *testing.T) {
	dir := runDir(t)
	e := Entry{ID: "notes", Name: "notes", PID: 5120, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	if err := Register(dir, e); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st, err := os.Lstat(EntryPath(dir, "notes"))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("entry mode is %o, want 600", got)
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Name() != "notes.json" {
		var got []string
		for _, n := range names {
			got = append(got, n.Name())
		}
		t.Fatalf("dir holds %v, want only notes.json (no temp leftovers)", got)
	}
}

func TestRegisterStampsZeroStartedAt(t *testing.T) {
	dir := runDir(t)
	e := Entry{ID: "notes", PID: 7, Socket: SocketPath(dir, "notes"), ProtocolVersion: "2.0"}
	if err := Register(dir, e); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := readEntry(dir, "notes.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt.IsZero() {
		t.Fatal("zero StartedAt should have been stamped from the clock")
	}
}

func TestRegisterKeepsSetStartedAt(t *testing.T) {
	dir := runDir(t)
	e := Entry{ID: "notes", PID: 7, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	if err := Register(dir, e); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := readEntry(dir, "notes.json")
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartedAt.Equal(fixedTime()) {
		t.Fatalf("StartedAt = %v, want the set value %v", got.StartedAt, fixedTime())
	}
}

func TestRegisterCreatesMissingDir(t *testing.T) {
	dir := t.TempDir() + "/husk"
	e := Entry{ID: "notes", PID: 7, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	if err := Register(dir, e); err != nil {
		t.Fatalf("Register: %v", err)
	}
	assertPrivateDir(t, dir)
}

func TestRegisterValidation(t *testing.T) {
	dir := runDir(t)
	cases := []struct {
		label string
		e     Entry
	}{
		{"invalid id", Entry{ID: "Foo_1", PID: 1, Socket: SocketPath(dir, "Foo_1")}},
		{"empty id", Entry{ID: "", PID: 1, Socket: SocketPath(dir, "")}},
		{"all digits name", Entry{ID: "notes", Name: "4242", PID: 1, Socket: SocketPath(dir, "notes")}},
		{"zero pid", Entry{ID: "notes", PID: 0, Socket: SocketPath(dir, "notes")}},
		{"empty socket", Entry{ID: "notes", PID: 1, Socket: ""}},
		{"socket elsewhere", Entry{ID: "notes", PID: 1, Socket: "/tmp/other.sock"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if err := Register(dir, tc.e); err == nil {
				t.Fatalf("Register(%+v) should fail", tc.e)
			}
		})
	}
}

func TestRegisterReplacesStaleEntry(t *testing.T) {
	// Whatever entry is on disk belongs to a dead run — the claim on the
	// name was made by binding the socket — so Register simply replaces it.
	dir := runDir(t)
	stale := Entry{ID: "notes", Name: "notes", PID: 111, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "1.5"}
	if err := writeEntryFile(dir, stale); err != nil {
		t.Fatal(err)
	}
	next := Entry{ID: "notes", Name: "notes", PID: 222, Socket: SocketPath(dir, "notes"), ProtocolVersion: "2.0"}
	if err := Register(dir, next); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := readEntry(dir, "notes.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 222 || got.ProtocolVersion != "2.0" {
		t.Fatalf("entry = %+v, want the new registration", got)
	}
}

func TestRegisterReplacesCorruptEntry(t *testing.T) {
	dir := runDir(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := Entry{ID: "notes", Name: "notes", PID: 222, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	if err := Register(dir, e); err != nil {
		t.Fatalf("Register over a corrupt entry: %v", err)
	}
	if _, err := readEntry(dir, "notes.json"); err != nil {
		t.Fatalf("entry should be readable now: %v", err)
	}
}

func TestRegisterSamePidOverLiveEntryReplaces(t *testing.T) {
	// The Server's own re-registration: bind made the name ours, so an
	// entry with our own pid is replaced without a probe.
	dir := runDir(t)
	mine := Entry{ID: "notes", Name: "notes", PID: 222, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "1.5"}
	if err := writeEntryFile(dir, mine); err != nil {
		t.Fatal(err)
	}
	next := mine
	next.ProtocolVersion = "2.0"
	if err := Register(dir, next); err != nil {
		t.Fatalf("Register over own live entry: %v", err)
	}
	got, err := readEntry(dir, "notes.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProtocolVersion != "2.0" {
		t.Fatalf("entry = %+v, want the replacement", got)
	}
}

func TestUnregister(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"notes.json", "notes.sock"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Unregister(dir, "notes"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	for _, f := range []string{"notes.json", "notes.sock"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s still exists", f)
		}
	}
	if err := Unregister(dir, "notes"); err != nil {
		t.Fatalf("second Unregister should be a no-op, got %v", err)
	}
}

func TestUnregisterRemovesSocketWithoutEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.sock"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Unregister(dir, "notes"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if _, err := os.Lstat(SocketPath(dir, "notes")); err == nil {
		t.Fatal("socket still exists")
	}
}

func TestRemoveIfStale(t *testing.T) {
	dir := t.TempDir()
	base := func(pid int, at time.Time) Entry {
		return Entry{ID: "notes", Name: "notes", PID: pid, Socket: SocketPath(dir, "notes"), StartedAt: at, ProtocolVersion: "2.0"}
	}
	cases := []struct {
		label    string
		file     Entry
		pinged   Entry
		wantGone bool
	}{
		{"same pid and started_at", base(111, fixedTime()), base(111, fixedTime()), true},
		{"different pid", base(111, fixedTime()), base(222, fixedTime()), false},
		{"different started_at", base(111, fixedTime()), base(111, fixedTime().Add(time.Minute)), false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			os.Remove(filepath.Join(dir, "notes.json"))
			os.Remove(filepath.Join(dir, "notes.sock"))
			if err := writeEntryFile(dir, tc.file); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "notes.sock"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := RemoveIfStale(dir, tc.pinged); err != nil {
				t.Fatalf("RemoveIfStale: %v", err)
			}
			_, err := os.Lstat(EntryPath(dir, "notes"))
			gone := err != nil
			if gone != tc.wantGone {
				t.Fatalf("entry gone = %v, want %v", gone, tc.wantGone)
			}
			if gone {
				if _, err := os.Lstat(SocketPath(dir, "notes")); err == nil {
					t.Fatal("socket should have been removed with the entry")
				}
			}
		})
	}
}

func TestRemoveIfStaleMissingEntryIsNoError(t *testing.T) {
	dir := t.TempDir()
	e := Entry{ID: "ghost", PID: 1, Socket: SocketPath(dir, "ghost"), StartedAt: fixedTime()}
	if err := RemoveIfStale(dir, e); err != nil {
		t.Fatalf("RemoveIfStale on a missing entry: %v", err)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	entries := []Entry{
		{ID: "notes", Name: "notes", PID: 5120, Socket: SocketPath(dir, "notes"), StartedAt: fixedTime(), ProtocolVersion: "2.0"},
		{ID: "4242", PID: 4242, Socket: SocketPath(dir, "4242"), StartedAt: fixedTime(), ProtocolVersion: "2.0"},
	}
	for _, e := range entries {
		if err := writeEntryFile(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "huge.json"), make([]byte, MaxEntryBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "incomplete.json"), []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.sock"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %+v, want the two valid entries only", got)
	}
	if got[0].ID != "4242" || got[1].ID != "notes" {
		t.Fatalf("List order = %q, %q; want sorted by id", got[0].ID, got[1].ID)
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	got, err := List(t.TempDir() + "/nowhere")
	if err != nil {
		t.Fatalf("List on a missing dir: %v", err)
	}
	if got != nil {
		t.Fatalf("List = %+v, want empty", got)
	}
}

func TestListSkipsSelfInconsistentEntries(t *testing.T) {
	// A syntactically valid JSON document whose id does not match the
	// file name, whose id is not a valid id, or whose socket is not the
	// instance's own SocketPath is corrupt: List skips it, Register may
	// replace it, stale cleanup may remove it.
	dir := t.TempDir()
	plant := func(base, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, base), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plant("other.json", `{"id":"x","pid":1,"socket":"`+SocketPath(dir, "x")+`"}`)
	plant("notes.json", `{"id":"notes","pid":1,"socket":"/somewhere/else.sock"}`)
	plant("Foo_1.json", `{"id":"Foo_1","pid":1,"socket":"`+SocketPath(dir, "Foo_1")+`"}`)
	good := Entry{ID: "4242", PID: 4242, Socket: SocketPath(dir, "4242"), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	if err := writeEntryFile(dir, good); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "4242" {
		t.Fatalf("List = %+v, want only the consistent 4242 entry", got)
	}
}

func TestUnregisterRejectsBadID(t *testing.T) {
	// Ids become file names; a mangled id must be refused, never joined
	// onto the directory.
	dir := t.TempDir()
	outside := filepath.Join(dir, "..", "escape.json")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "", "Foo_1", "notes/x"} {
		if err := Unregister(dir, id); err == nil {
			t.Errorf("Unregister(%q) should be refused", id)
		}
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Error("the file outside dir must still exist")
	}
}
