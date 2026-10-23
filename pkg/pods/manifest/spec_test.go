package manifest

import (
	"path/filepath"
	"testing"
)

func TestProcessSpecValidationAndCommandResolution(t *testing.T) {
	dir := t.TempDir()
	s := Spec{ID: "plugin-A.1", Protocol: 2, Command: []string{"./plugin", "./data", "--unchanged"}, WorkDir: dir}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	argv := s.Argv()
	if argv[0] != filepath.Join(dir, "plugin") || argv[1] != "./data" || s.Command[0] != "./plugin" {
		t.Fatal(argv)
	}
	clone := s.Clone()
	clone.Command[0] = "changed"
	if s.Command[0] != "./plugin" {
		t.Fatal("Clone aliased argv")
	}
	for _, change := range []func(*Spec){func(s *Spec) { s.ID = "../bad" }, func(s *Spec) { s.Protocol = 1 }, func(s *Spec) { s.Command = nil }, func(s *Spec) { s.WorkDir = "relative" }} {
		bad := s.Clone()
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("accepted invalid process spec")
		}
	}
}
func TestPluginIDGrammar(t *testing.T) {
	for _, id := range []string{"a", "A_2.-", "0"} {
		if !ValidID(id) {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"", "_bad", "a/b", "a b", "é"} {
		if ValidID(id) {
			t.Fatal(id)
		}
	}
}
