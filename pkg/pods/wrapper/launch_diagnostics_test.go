//go:build linux || darwin

package wrapper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestLaunchDiagnosticWithMissingWorkDirectory(t *testing.T) {
	for _, removeDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_work_directory=%t", removeDir), func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "plugin")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "stderr.log")
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			spec := manifest.Spec{ID: "p", Protocol: protocol.Major, Command: []string{"./plugin"}, CommandName: "./plugin", WorkDir: dir}
			spec.Command = spec.Argv()
			w, err := New(spec, Options{LogPath: func(string) (string, error) { return logPath, nil }})
			if err != nil {
				t.Fatal(err)
			}
			if removeDir {
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
			}
			want := fmt.Sprintf("command %q not found in %s; build it first", "./plugin", dir)
			if removeDir {
				// Group configuration sets SysProcAttr, bypassing Go's optional
				// chdir precheck. Preserve the native error for this launch shape.
				cmd := exec.Command(spec.Command[0])
				cmd.Dir = dir
				if err := configureGroup(cmd); err != nil {
					t.Fatal(err)
				}
				nativeErr := cmd.Start()
				if !errors.Is(nativeErr, os.ErrNotExist) {
					t.Fatalf("native start error = %v, want ENOENT", nativeErr)
				}
				want = nativeErr.Error()
				t.Logf("native missing-work-directory error: %s", want)
			}
			err = w.Start(nil)
			<-w.Done()
			if err == nil || err.Error() != want {
				t.Errorf("Start error = %v, want %q", err, want)
			}
			if info := w.Info(); info.Status != Failed || info.LastError != want {
				t.Errorf("status=%q last_error=%q, want failed with %q", info.Status, info.LastError, want)
			}
		})
	}
}
