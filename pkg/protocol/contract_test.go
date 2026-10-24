package protocol

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func repositoryFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(append([]string{dir}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestErrorCodeCompleteness(t *testing.T) {
	spec := string(repositoryFile(t, "docs", "docs", "design", "protocol.md"))
	start := strings.Index(spec, "### Error codes")
	end := strings.Index(spec[start:], "### Answering bad input") + start
	rows := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(spec[start:end], -1)
	if len(rows) != 15 {
		t.Fatalf("spec has %d error codes", len(rows))
	}
	codes := Codes()
	if len(codes) != len(rows) {
		t.Fatalf("Codes = %v", codes)
	}
	for _, row := range rows {
		if !Code(row[1]).Valid() || !slices.Contains(codes, Code(row[1])) {
			t.Errorf("missing %s", row[1])
		}
	}
	for _, c := range []Code{"", "future", "FAILED"} {
		if c.Valid() {
			t.Errorf("accepted %q", c)
		}
	}
	codes[0] = "mutated"
	if Codes()[0] != CodeParseError {
		t.Fatal("Codes exposes shared state")
	}
}

func TestMethodConstantsCoverSpec(t *testing.T) {
	spec := string(repositoryFile(t, "docs", "docs", "design", "protocol.md"))
	start := strings.Index(spec, "| Namespace | Methods |")
	end := strings.Index(spec[start:], "Declarations (") + start
	names := regexp.MustCompile("`([a-z]+\\.[a-z_]+)`").FindAllStringSubmatch(spec[start:end], -1)
	// Direction prose repeats event.deliver; count the unique method vocabulary.
	declared := make(map[string]bool)
	for _, name := range names {
		declared[name[1]] = true
	}
	methods := Methods()
	if len(declared) != len(methods) {
		t.Fatalf("spec has %d methods, package has %d", len(declared), len(methods))
	}
	seen := make(map[string]bool)
	for _, method := range methods {
		if seen[method] {
			t.Errorf("duplicate %s", method)
		}
		seen[method] = true
		if err := ValidateMethod(method); err != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	for _, name := range names {
		if !seen[name[1]] {
			t.Errorf("missing %s", name[1])
		}
	}
}

func TestMethodValidation(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"a.b", true}, {"a0_-._-9", true}, {"future.verb", true},
		{strings.Repeat("a", 254) + ".b", true}, {strings.Repeat("a", 255) + ".b", false},
		{"", false}, {"a", false}, {".a", false}, {"a.", false},
		{"a.b.c", false}, {"a..b", false}, {"A.b", false}, {"a/B.c", false},
		{"a.b c", false}, {"é.b", false}, {"a.b\n", false}, {"a.b\x00", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateMethod(tc.name); (err == nil) != tc.valid {
				t.Fatalf("ValidateMethod = %v, valid=%v", err, tc.valid)
			}
		})
	}
}

func TestVersionMatrix(t *testing.T) {
	for _, tc := range []struct {
		v                 string
		major, minor      int
		parse, compatible bool
	}{
		{"2.0", 2, 0, true, true}, {"2.1", 2, 1, true, true}, {"2", 2, 0, true, true}, {"2.99", 2, 99, true, true},
		{"02.001", 2, 1, true, true}, {"1.2", 1, 2, true, false}, {"3", 3, 0, true, false},
		{"0.0", 0, 0, true, false}, {"", 0, 0, false, false}, {"2.", 0, 0, false, false},
		{".0", 0, 0, false, false}, {"2.0.1", 0, 0, false, false}, {"-2.0", 0, 0, false, false},
		{"+2.0", 0, 0, false, false}, {" 2.0", 0, 0, false, false}, {"2.0\n", 0, 0, false, false},
		{"2.x", 0, 0, false, false}, {"999999999999999999999.0", 0, 0, false, false},
	} {
		t.Run(tc.v, func(t *testing.T) {
			major, minor, err := ParseVersion(tc.v)
			if (err == nil) != tc.parse || major != tc.major || minor != tc.minor {
				t.Fatalf("ParseVersion = %d,%d,%v", major, minor, err)
			}
			err = CheckVersion(tc.v)
			if (err == nil) != tc.compatible {
				t.Fatalf("CheckVersion = %v", err)
			}
			if err != nil {
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != CodeVersionMismatch {
					t.Fatal(err)
				}
			}
		})
	}
	if Version != "2.8" || Major != 2 {
		t.Fatal("unexpected version")
	}
}

func TestProtocolPackageImportsStandardLibraryOnly(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "github.com/hyperlab-sh/husk/pkg/protocol")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "github.com/hyperlab-sh/husk/pkg/protocol" {
		t.Fatalf("non-standard dependency: %s", out)
	}
}

func TestParamValidation(t *testing.T) {
	for _, phase := range []KeyPhase{KeyPhaseDown, KeyPhaseUp, KeyPhaseRepeat, KeyPhaseSync} {
		if !phase.Valid() || (KeyStateParams{Phase: phase, Key: "a"}).Validate() != nil {
			t.Fatal(phase)
		}
	}
	for _, p := range []KeyStateParams{{}, {Phase: "future", Key: "a"}, {Phase: KeyPhaseDown}, {Phase: KeyPhaseDown, Key: "a", Surface: "future"}} {
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	for _, p := range []SurfaceKeyParams{{}, {Surface: SurfaceMainStage}, {Surface: "future", Key: "a"}} {
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	if err := (SurfaceKeyParams{Surface: SurfaceMainStage, Key: "a"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, surface := range []SurfaceID{SurfaceActivityRail, SurfaceSidebarNavigation, SurfaceSidebarList, SurfaceThreadRowStatus, SurfaceSidebarFooter, SurfaceMainHeader, SurfaceMainStage, SurfaceSidePanel, SurfaceAppOverlay, SurfaceModal, SurfaceFullCanvas, SurfaceWorkspaceStage} {
		if !surface.Valid() {
			t.Fatal(surface)
		}
	}
}

func TestSurfaceUpdateCaptureRoundTrip(t *testing.T) {
	yes, no := CaptureTrue, CaptureFalse
	text, all := CaptureValueText, CaptureValueAll
	for _, tc := range []struct {
		name string
		in   string
		want *CaptureValue
		out  string // the capture member after re-marshalling; "" when absent
	}{
		{"absent", `{"surface":"main-stage","lines":[]}`, nil, ""},
		{"false", `{"surface":"main-stage","lines":[],"capture":false}`, &no, "false"},
		{"true", `{"surface":"main-stage","lines":[],"capture":true}`, &yes, "true"},
		{"text", `{"surface":"main-stage","lines":[],"capture":"text"}`, &text, `"text"`},
		{"all", `{"surface":"main-stage","lines":[],"capture":"all"}`, &all, `"all"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p SurfaceUpdateParams
			if err := json.Unmarshal([]byte(tc.in), &p); err != nil {
				t.Fatal(err)
			}
			if (p.Capture == nil) != (tc.want == nil) || p.Capture != nil && *p.Capture != *tc.want {
				t.Fatalf("Capture = %v, want %v", p.Capture, tc.want)
			}
			if p.Capture != nil {
				wantMode := CaptureNone
				switch tc.name {
				case "true", "text":
					wantMode = CaptureText
				case "all":
					wantMode = CaptureAll
				}
				if p.Capture.Mode() != wantMode {
					t.Fatalf("mode %q, want %q", p.Capture.Mode(), wantMode)
				}
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			_ = json.Unmarshal(b, &m)
			got, has := m["capture"]
			if tc.out == "" && has || tc.out != "" && string(got) != tc.out {
				t.Fatalf("re-marshalled capture = %q (present %v), want %q", got, has, tc.out)
			}
		})
	}
}

func TestWireSchemaCarriesSurfaceUpdateCapture(t *testing.T) {
	raw := repositoryFile(t, "docs", "docs", "wire-schema-v2.json")
	var doc struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	def := doc.Defs["SurfaceUpdateParams"]
	if got := string(def.Properties["capture"]); !strings.Contains(got, `"boolean"`) {
		t.Fatalf("schema SurfaceUpdateParams.capture = %s", got)
	}
	if !slices.Contains(def.Required, "lines") || slices.Contains(def.Required, "capture") {
		t.Fatalf("required = %v, want lines and not capture", def.Required)
	}
}
