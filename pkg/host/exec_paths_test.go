package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Only converter deadlines fire; unrelated host timers stay on frozen time.
// Inline cancellation makes even a transient timeout deterministic and proves
// construction does not retry it, without waiting for wall-clock time.
type pathsConverterClock struct {
	frozenClock
	expire    bool
	deadlines atomic.Int32
}

func (c *pathsConverterClock) AfterFunc(d time.Duration, fn func()) protocol.Timer {
	if d == 5*time.Second {
		c.deadlines.Add(1)
		if c.expire {
			fn()
		}
	}
	return neverTimer{}
}

func TestHostExecPathsFailureAbortsStartup(t *testing.T) {
	for _, source := range []string{"configured", "replacement", "additive"} {
		t.Run(source, func(t *testing.T) {
			for _, mode := range []string{"bad", "detect-bad", "invalid-document", "deadline"} {
				t.Run(mode, func(t *testing.T) {
					t.Setenv("GORACE", "atexit_sleep_ms=0")
					exe, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					root := t.TempDir()
					dir := filepath.Join(root, mode)
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "format"), []byte(filepath.Base(exe)), 0600); err != nil {
						t.Fatal(err)
					}
					trace := filepath.Join(root, "trace")
					t.Setenv("CONVERTER_TRACE", trace)
					t.Setenv("CONVERTER_DISCOVER", dir+"\n")
					clock := &pathsConverterClock{frozenClock: frozenClock{time.Unix(1000, 0)}, expire: mode == "deadline"}
					var failed *Root
					options := []Option{WithConverters([]string{exe}), func(o *options) { o.afterFail = func(r *Root) { failed = r } }}
					if source == "additive" {
						options = append(options, WithPluginAdditions(map[string][]string{"paths": {dir}}))
					}
					h, err := boot(t, func(c *contracts.Config) {
						c.Clock = clock
						switch source {
						case "configured":
							config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk")
							if err := os.MkdirAll(config, 0700); err != nil {
								t.Fatal(err)
							}
							raw, _ := json.Marshal(map[string]any{"husk": map[string]any{"plugins": map[string]any{"paths": []string{dir}}}})
							if err := os.WriteFile(filepath.Join(config, "config.json"), raw, 0600); err != nil {
								t.Fatal(err)
							}
						case "replacement":
							pluginSettings(c)["paths"] = []string{dir}
						}
					}, options...)
					layer := "instance"
					if source == "configured" {
						layer = "user"
					}
					if err == nil || h.root != nil {
						t.Fatalf("failed paths entry started a host: %v", err)
					}
					var failure *manifest.ConverterError
					if !errors.As(err, &failure) || !strings.Contains(err.Error(), "husk.plugins.paths[0]") || !strings.Contains(err.Error(), "(layer "+layer+")") || !strings.Contains(err.Error(), "converter_failed") {
						t.Fatalf("startup error=%v", err)
					}
					want := map[string]string{"bad": "exit status 6", "detect-bad": "exit status 7", "invalid-document": "exit status 0", "deadline": "deadline exceeded after 5s"}[mode]
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("startup error=%v want=%s", err, want)
					}
					if failed == nil || !storeStopped(failed) {
						t.Fatal("failed construction did not close Store")
					}
					if refusals := failed.Frontend().Refusals(); len(refusals) != 0 {
						t.Fatalf("fatal path was treated as a refusal: %+v", refusals)
					}
					if mode == "deadline" {
						if got := clock.deadlines.Load(); got != 1 {
							t.Fatalf("deadline attempts=%d", got)
						}
						return
					}
					raw, err := os.ReadFile(trace)
					if err != nil {
						t.Fatal(err)
					}
					text := string(raw)
					if strings.Count(text, ":detect "+dir+"\n") != 1 || strings.Contains(text, ":discover\n") {
						t.Fatalf("failure retried or discovery ran: %s", text)
					}
					converts := 1
					if mode == "detect-bad" {
						converts = 0
					}
					if strings.Count(text, ":convert "+dir+"\n") != converts {
						t.Fatalf("convert attempts: %s", text)
					}
				})
			}
		})
	}
}
