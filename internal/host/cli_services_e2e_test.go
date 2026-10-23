package host

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/cli"
)

func TestServicesCLIAgainstRealHost(t *testing.T) {
	logs := &logSink{}
	h := bootWithSecret(t, logs)
	for _, tc := range []struct {
		args  []string
		check func(map[string]json.RawMessage) bool
	}{
		{[]string{"settings", "get", "theme.allow_low_contrast"}, func(r map[string]json.RawMessage) bool { return string(r["value"]) == "false" }},
		{[]string{"settings", "validate"}, func(r map[string]json.RawMessage) bool { return string(r["ok"]) == "true" }},
		{[]string{"config", "reload"}, func(r map[string]json.RawMessage) bool {
			var reports []json.RawMessage
			return json.Unmarshal(r["reports"], &reports) == nil && len(reports) == 5
		}},
		{[]string{"keys", "--problems"}, func(r map[string]json.RawMessage) bool { return len(r) == 1 && r["problems"] != nil }},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			code, stdout, _ := cliRun(h, append(tc.args, "--format", "json")...)
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			var result map[string]json.RawMessage
			if json.Unmarshal([]byte(stdout), &result) != nil || !tc.check(result) {
				t.Fatal("unexpected host result")
			}
		})
	}
	t.Run("roles", func(t *testing.T) {
		code, stdout, _ := cliRun(h, "theme", "roles", "--format", "json")
		var roles []string
		if code != 0 || json.Unmarshal([]byte(stdout), &roles) != nil || len(roles) != 19 || roles[0] != "accent" {
			t.Fatal("role projection")
		}
	})
	t.Run("current", func(t *testing.T) {
		code, stdout, _ := cliRun(h, "profile", "current", "--format", "json")
		if code != 0 || stdout != "null\n" {
			t.Fatal("no active profile projection")
		}
	})
	t.Run("secret stdin and metadata", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cli.Main(context.Background(), []string{"secrets", "set", "tok.en", "--format", "json"}, cli.Env{
			Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("fake-token-1\n"), Getenv: func(string) string { return "" }, RuntimeDir: func() (string, bool, error) { return h.dir, true, nil },
		})
		if code != 0 {
			t.Fatalf("secret set exit %d", code)
		}
		code, out, _ := cliRun(h, "secrets", "list", "--format", "json")
		var result struct {
			Storage string
			Secrets []struct {
				Name string
				Set  *bool
			}
		}
		if code != 0 || json.Unmarshal([]byte(out), &result) != nil || result.Storage != "ok" || len(result.Secrets) != 1 || result.Secrets[0].Name != "tok.en" || result.Secrets[0].Set == nil || !*result.Secrets[0].Set {
			t.Fatal("secret metadata mismatch")
		}
		if strings.Contains(out, "fake-token-1") || strings.Contains(logs.all(), "fake-token-1") {
			t.Fatal("secret containment failure")
		}
	})
}
