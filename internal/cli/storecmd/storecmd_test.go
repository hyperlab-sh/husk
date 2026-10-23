package storecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// memKeyring is the fake keyring: the real OS keyring is never touched.
type memKeyring struct {
	mu    sync.Mutex
	value string
}

func (k *memKeyring) Get(_, _ string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.value == "" {
		return "", keyring.ErrNotFound
	}
	return k.value, nil
}
func (k *memKeyring) Set(_, _, v string) error { k.mu.Lock(); k.value = v; k.mu.Unlock(); return nil }
func (k *memKeyring) Delete(_, _ string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.value == "" {
		return keyring.ErrNotFound
	}
	k.value = ""
	return nil
}

// fx is one private world: data, state and config trees under a temp root and
// a runtime directory in a short /tmp path.
type fx struct {
	t       *testing.T
	root    string
	runtime string
	ring    *memKeyring
	probe   func(socket string) error
	out     bytes.Buffer
	errb    bytes.Buffer
}

func newFx(t *testing.T) *fx {
	t.Helper()
	rt, err := os.MkdirTemp("/tmp", "sc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	return &fx{t: t, root: t.TempDir(), runtime: rt, ring: &memKeyring{},
		probe: func(string) error { return instance.ErrSocketMissing }}
}

func (f *fx) deps() deps {
	return deps{
		runtimeDir: func() (string, bool, error) { return f.runtime, true, nil },
		env: store.Env{
			LookupEnv: func(k string) (string, bool) {
				switch k {
				case "XDG_CONFIG_HOME":
					return filepath.Join(f.root, "config"), true
				case "XDG_STATE_HOME":
					return filepath.Join(f.root, "state"), true
				case "XDG_DATA_HOME":
					return filepath.Join(f.root, "data"), true
				}
				return "", false
			},
			HomeDir: func() (string, error) { return f.root, nil },
		},
		keyring:  f.ring,
		instance: &instance.Deps{Ping: func(s string) error { return f.probe(s) }},
	}
}

func (f *fx) run(format output.Format, args ...string) error {
	f.out.Reset()
	f.errb.Reset()
	return command(f.deps()).Run(context.Background(), args, output.Options{
		Format: format, Stdout: &f.out, Stderr: &f.errb, Stdin: strings.NewReader(""),
		Getenv: func(string) string { return "" },
	})
}

func (f *fx) register(id string) {
	f.t.Helper()
	err := instance.Register(f.runtime, instance.Entry{ID: id, PID: os.Getpid(), Socket: instance.SocketPath(f.runtime, id), ProtocolVersion: "2.0"})
	if err != nil {
		f.t.Fatal(err)
	}
}

func asErr(t *testing.T, err error) *output.Error {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not an *output.Error", err)
	}
	return e
}

func (f *fx) dataFile(parts ...string) string {
	return filepath.Join(append([]string{f.root, "data", "husk"}, parts...)...)
}

func TestPathsPrintsEveryDirectoryInBothFormats(t *testing.T) {
	f := newFx(t)
	if err := f.run(output.JSON, "paths"); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(f.out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"config":  filepath.Join(f.root, "config", "husk"),
		"state":   filepath.Join(f.root, "state", "husk"),
		"data":    filepath.Join(f.root, "data", "husk"),
		"runtime": f.runtime,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if err := f.run(output.Table, "paths"); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if !strings.Contains(f.out.String(), k) || !strings.Contains(f.out.String(), v) {
			t.Errorf("table lacks %s %s:\n%s", k, v, f.out.String())
		}
	}
	if f.errb.Len() != 0 {
		t.Errorf("stderr = %q", f.errb.String())
	}
}

// TestStoreCommandsCreateNoRuntimeDirectory runs the production Command on a
// pristine HOME and XDG: the runtime directory must not exist afterwards.
// (Store creates its config, state and data directories by design, store.md.)
func TestStoreCommandsCreateNoRuntimeDirectory(t *testing.T) {
	for _, verb := range []string{"paths", "check", "gc"} {
		t.Run(verb, func(t *testing.T) {
			home := t.TempDir()
			xdg := filepath.Join(home, "run")
			t.Setenv("HOME", home)
			t.Setenv("XDG_RUNTIME_DIR", xdg)
			for _, k := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
				t.Setenv(k, "")
			}
			var out, errb bytes.Buffer
			err := Command().Run(context.Background(), []string{verb}, output.Options{
				Format: output.JSON, Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Lstat(xdg); !os.IsNotExist(statErr) {
				t.Fatalf("runtime directory exists after store %s: %v", verb, statErr)
			}
		})
	}
}

// TestResetSecretsCreatesNoRuntimeDirectory covers reset-secrets separately:
// it runs on the fake keyring, never the production Command, whose keyring is
// the real OS one.
func TestResetSecretsCreatesNoRuntimeDirectory(t *testing.T) {
	f := newFx(t)
	absent := filepath.Join(f.root, "run")
	d := f.deps()
	d.runtimeDir = func() (string, bool, error) { return absent, false, nil }
	err := command(d).Run(context.Background(), []string{"reset-secrets"}, output.Options{Format: output.JSON, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(absent); !os.IsNotExist(statErr) {
		t.Fatalf("runtime directory exists: %v", statErr)
	}
}

func TestAbsentRuntimeDirectoryPassesStoreNoRuntime(t *testing.T) {
	f := newFx(t)
	d := f.deps()
	d.runtimeDir = func() (string, bool, error) { return "/does/not/exist", false, nil }
	var out bytes.Buffer
	err := command(d).Run(context.Background(), []string{"paths"}, output.Options{Format: output.JSON, Stdout: &out, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	_ = json.Unmarshal(out.Bytes(), &got)
	if got["runtime"] != "" {
		t.Fatalf("runtime = %q, want empty", got["runtime"])
	}
}

func TestUnusableRuntimeDirectoryIsExit7(t *testing.T) {
	f := newFx(t)
	d := f.deps()
	d.runtimeDir = func() (string, bool, error) { return "", false, errors.New("runtime dir /x is not private to uid 1") }
	for _, verb := range []string{"paths", "check", "gc", "reset-secrets"} {
		err := command(d).Run(context.Background(), []string{verb}, output.Options{Format: output.JSON, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
		if e := asErr(t, err); e.Exit != output.ExitEnvironment || e.Code != output.CodeEnvironment {
			t.Errorf("%s: %+v", verb, e)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	f := newFx(t)
	for _, args := range [][]string{{}, {"nope"}, {"paths", "x"}, {"check", "--x"}, {"gc", "x"}, {"reset-secrets", "x"}} {
		e := asErr(t, f.run(output.JSON, args...))
		if e.Exit != output.ExitUsage {
			t.Errorf("%v: exit %d", args, e.Exit)
		}
	}
}

func TestStoreFailureIsExit1StoreCode(t *testing.T) {
	f := newFx(t)
	d := f.deps()
	d.env.LookupEnv = func(k string) (string, bool) { return "relative/path", k == "XDG_DATA_HOME" }
	err := command(d).Run(context.Background(), []string{"paths"}, output.Options{Format: output.JSON, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	e := asErr(t, err)
	if e.Exit != output.ExitRemote || e.Code != output.CodeStore || !strings.Contains(e.Message, "must be absolute") {
		t.Fatalf("%+v", e)
	}
}

// seedSliceDir makes <state>/user/slices/old.ns, the directory of a slice, with
// an expired temp file and a quarantined copy.
func (f *fx) seedSliceDir() (dir, temp, corrupt string) {
	f.t.Helper()
	dir = filepath.Join(f.root, "state", "husk", "user", "slices", "old.ns")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	for p := dir; p != filepath.Join(f.root, "state"); p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o700)
	}
	temp = filepath.Join(dir, ".current.json.tmp-1000000000")
	corrupt = filepath.Join(dir, "current.json.corrupt-20200101T000000.000000000Z")
	for _, p := range []string{temp, corrupt} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	_ = os.Chtimes(temp, old, old)
	return dir, temp, corrupt
}

func TestCheckListsAndDeletesNothing(t *testing.T) {
	f := newFx(t)
	_, temp, corrupt := f.seedSliceDir()
	if err := f.run(output.JSON, "check"); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Problems, Quarantined, Orphans []json.RawMessage
	}
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Quarantined) != 1 || !strings.Contains(f.out.String(), "current.json.corrupt-") {
		t.Fatalf("check output = %s", f.out.String())
	}
	for _, p := range []string{temp, corrupt} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("check deleted %s: %v", p, err)
		}
	}
	// An empty store prints [] lists, never null.
	g := newFx(t)
	if err := g.run(output.JSON, "check"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(g.out.String(), "null") {
		t.Fatalf("empty check printed null: %s", g.out.String())
	}
	if err := g.run(output.Table, "check"); err != nil || g.out.Len() == 0 {
		t.Fatalf("table check: %v %q", err, g.out.String())
	}
}

func TestGCRemovesExpiredTempsOnly(t *testing.T) {
	f := newFx(t)
	_, temp, corrupt := f.seedSliceDir()
	if err := f.run(output.JSON, "gc"); err != nil {
		t.Fatal(err)
	}
	var rep struct{ Removed, Problems []json.RawMessage }
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Problems == nil {
		t.Fatalf("gc output = %s", f.out.String())
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Errorf("expired temp survived gc: %v", err)
	}
	if _, err := os.Stat(corrupt); err != nil {
		t.Errorf("gc removed a fresh-enough quarantine: %v", err)
	}
	if err := f.run(output.Table, "gc"); err != nil {
		t.Fatal(err)
	}
}

// sealOne seals a namespace under the fake keyring and lays down bystander
// files that a reset must not touch.
func (f *fx) sealOne() (sealed string, bystanders []string) {
	f.t.Helper()
	s, err := store.Open(store.Options{RuntimeDir: f.runtime, Env: f.deps().env, Keyring: f.ring})
	if err != nil {
		f.t.Fatal(err)
	}
	doc, _ := s.Secrets("tokens")
	if err := doc.Put("k", []byte("v")); err != nil {
		f.t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	for _, p := range []string{
		f.dataFile("other.txt"),
		filepath.Join(f.root, "state", "husk", "other.txt"),
		filepath.Join(f.root, "config", "husk", "other.txt"),
	} {
		if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
			f.t.Fatal(err)
		}
		bystanders = append(bystanders, p)
	}
	return f.dataFile("secrets", "tokens.age"), bystanders
}

func TestResetSecretsRefusedWhileAnInstanceRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		ping func(string) error
		ids  []string
		want string
	}{
		{"live", func(string) error { return nil }, []string{"notes"}, "1 instance is registered (notes); stop it first"},
		{"unresponsive", func(string) error { return errors.New("timeout") }, []string{"notes"}, "1 instance is registered (notes); stop it first"},
		{"two", func(string) error { return nil }, []string{"4242", "notes"}, "2 instances are registered (4242, notes); stop them first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			sealed, bystanders := f.sealOne()
			for _, id := range tc.ids {
				f.register(id)
			}
			f.probe = tc.ping
			e := asErr(t, f.run(output.JSON, "reset-secrets"))
			if e.Exit != output.ExitRemote || e.Code != output.CodeStore || e.Message != tc.want {
				t.Fatalf("%+v", e)
			}
			for _, p := range append(bystanders, sealed) {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("refused reset still touched %s: %v", p, err)
				}
			}
			if f.ring.value == "" {
				t.Error("refused reset deleted the keyring identity")
			}
		})
	}
}

func TestResetSecretsWithNoInstanceDeletesSecretsAndNothingElse(t *testing.T) {
	f := newFx(t)
	sealed, bystanders := f.sealOne()
	_, temp, corrupt := f.seedSliceDir() // expired: only a GC would remove them
	bystanders = append(bystanders, temp, corrupt)
	f.register("gone") // stale: the probe says nothing listens, Survey removes the entry
	if err := f.run(output.JSON, "reset-secrets"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(f.out.String()) != `{"reset":true}` {
		t.Fatalf("stdout = %q", f.out.String())
	}
	if _, err := os.Lstat(sealed); !os.IsNotExist(err) {
		t.Errorf("sealed namespace survived: %v", err)
	}
	if f.ring.value != "" {
		t.Error("keyring identity survived")
	}
	for _, p := range bystanders {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("reset touched %s: %v", p, err)
		}
	}
}

func TestStoreAcceptsFormatTableForReset(t *testing.T) {
	f := newFx(t)
	if err := f.run(output.Table, "reset-secrets"); err != nil || strings.TrimSpace(f.out.String()) == "" {
		t.Fatalf("%v %q", err, f.out.String())
	}
}

// TestResetSecretsSurveyFailureIsExit7: a runtime directory the survey cannot
// read is the environment failure (instances.md rule 9), and nothing is deleted.
func TestResetSecretsSurveyFailureIsExit7(t *testing.T) {
	f := newFx(t)
	sealed, bystanders := f.sealOne()
	notADir := filepath.Join(f.root, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := f.deps()
	d.runtimeDir = func() (string, bool, error) { return notADir, true, nil }
	err := command(d).Run(context.Background(), []string{"reset-secrets"}, output.Options{Format: output.JSON, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if e := asErr(t, err); e.Exit != output.ExitEnvironment || e.Code != output.CodeEnvironment {
		t.Fatalf("%+v", e)
	}
	for _, p := range append(bystanders, sealed) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("failed survey still touched %s: %v", p, err)
		}
	}
	if f.ring.value == "" {
		t.Error("failed survey deleted the keyring identity")
	}
}

// TestCheckListsACorruptRecordAndMovesNothing: an unparsable slice record is
// reported by check and stays exactly where and what it was.
func TestCheckListsACorruptRecordAndMovesNothing(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(f.root, "state", "husk", "user", "slices", "x.y")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for p := dir; p != filepath.Join(f.root, "state"); p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o700)
	}
	file := filepath.Join(dir, "current.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(output.JSON, "check"); err != nil {
		t.Fatal(err)
	}
	var rep struct{ Problems, Quarantined []json.RawMessage }
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Problems) == 0 || !strings.Contains(f.out.String(), "x.y") || len(rep.Quarantined) != 0 {
		t.Fatalf("check output = %s", f.out.String())
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "{not json" {
		t.Fatalf("record changed: %q %v", got, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory changed: %v", entries)
	}
}
