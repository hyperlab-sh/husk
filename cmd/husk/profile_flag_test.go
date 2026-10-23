package main

import "testing"

func TestStartProfile(t *testing.T) {
	if p, err := startProfile("", false); p != nil || err != nil {
		t.Fatalf("absent flag = %v, %v", p, err)
	}
	for _, ok := range []string{"work", "a_b-1"} {
		if p, err := startProfile(ok, true); err != nil || p == nil || *p != ok {
			t.Errorf("%q = %v, %v", ok, p, err)
		}
	}
	// A given empty name is invalid, like an empty --instance.
	for _, bad := range []string{"", "Work", "a/b", "..", "a b", "é"} {
		if p, err := startProfile(bad, true); err == nil || p != nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
