package huskipc

import (
	"encoding/json"
	"go/build"
	"strings"
	"testing"
)

func TestProtocolVersionConstantsAgree(t *testing.T) {
	major, _, err := ParseProtocolVersion(ProtocolVersion)
	if err != nil || major != ProtocolMajor {
		t.Fatalf("ProtocolVersion %q parses to major %d (%v), want %d", ProtocolVersion, major, err, ProtocolMajor)
	}
}

func TestParseProtocolVersion(t *testing.T) {
	ok := map[string][2]int{"1": {1, 0}, "1.0": {1, 0}, "1.7": {1, 7}, "02.10": {2, 10}}
	for v, want := range ok {
		major, minor, err := ParseProtocolVersion(v)
		if err != nil || major != want[0] || minor != want[1] {
			t.Errorf("ParseProtocolVersion(%q) = %d.%d, %v; want %d.%d", v, major, minor, err, want[0], want[1])
		}
	}
	for _, v := range []string{"", "one", "1.x", "-1", "+1", "1.", ".1", " 1", "1.0.0", "v1", "99999999999999999999"} {
		if _, _, err := ParseProtocolVersion(v); err == nil {
			t.Errorf("ParseProtocolVersion(%q) accepted an invalid version", v)
		} else if !strings.Contains(err.Error(), "want MAJOR.MINOR") {
			t.Errorf("ParseProtocolVersion(%q) error %q does not explain the format", v, err)
		}
	}
}

func TestCheckProtocolVersion(t *testing.T) {
	for _, v := range []string{"", "1", "1.0", "1.9"} {
		if err := CheckProtocolVersion(v); err != nil {
			t.Errorf("CheckProtocolVersion(%q) = %v, want compatible", v, err)
		}
	}
	for v, want := range map[string]string{
		"2.0": `unsupported protocol_version "2.0": host speaks 1.1 (major 1)`,
		"0.9": `unsupported protocol_version "0.9": host speaks 1.1 (major 1)`,
		"one": `invalid protocol_version "one": want MAJOR.MINOR`,
	} {
		err := CheckProtocolVersion(v)
		if err == nil || err.Error() != want {
			t.Errorf("CheckProtocolVersion(%q) = %v, want %q", v, err, want)
		}
	}
}

// protocol_version is additive: a plugin that never sets it produces the same
// hello bytes as before, and a welcome only carries it when the host sets it.
func TestProtocolVersionIsAdditive(t *testing.T) {
	keys := func(v any) map[string]json.RawMessage {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var k map[string]json.RawMessage
		if err := json.Unmarshal(data, &k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	if _, found := keys(HelloPayload{PluginID: "a"})["protocol_version"]; found {
		t.Fatal("protocol_version present on a hello that did not set it")
	}
	if got := string(keys(HelloPayload{PluginID: "a", ProtocolVersion: "1.2"})["protocol_version"]); got != `"1.2"` {
		t.Fatalf("hello protocol_version = %s", got)
	}
	if _, found := keys(WelcomePayload{})["protocol_version"]; found {
		t.Fatal("protocol_version present on a zero welcome")
	}
	w := keys(WelcomePayload{ProtocolVersion: ProtocolVersion, HostVersion: "0.1.0"})
	if string(w["protocol_version"]) != `"1.1"` || string(w["host_version"]) != `"0.1.0"` {
		t.Fatalf("welcome = protocol_version %s host_version %s", w["protocol_version"], w["host_version"])
	}

	// A pre-versioning hello (as Dolphin's crate sends it) decodes with an
	// empty version, which the host accepts.
	var env Envelope
	if err := json.Unmarshal([]byte(`{"type":"hello","hello":{"plugin_id":"example.plugin","slots":["sidebar-list"]}}`), &env); err != nil {
		t.Fatal(err)
	}
	if env.Hello.ProtocolVersion != "" || CheckProtocolVersion(env.Hello.ProtocolVersion) != nil {
		t.Fatalf("legacy hello version %q rejected", env.Hello.ProtocolVersion)
	}
}

// The wire package must stay importable by any Go plugin without dragging in
// rendering code: it imports the standard library only (so lipgloss, Bubble
// Tea and the host's frame/theme packages are all excluded, directly and
// transitively).
func TestWirePackageImportsStandardLibraryOnly(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Imports) == 0 {
		t.Fatal("no imports found; the test is not looking at the package")
	}
	for _, imp := range pkg.Imports {
		first, _, _ := strings.Cut(imp, "/")
		if strings.Contains(first, ".") {
			t.Errorf("pkg/huskipc imports non-standard package %q", imp)
		}
	}
}
