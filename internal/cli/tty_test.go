package cli

import (
	"os"
	"testing"
)

func TestIsTerminal(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	regular, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()

	for _, tc := range []struct {
		name string
		f    *os.File
		want bool
	}{
		{"/dev/null is a character device, not a terminal", devnull, false},
		{"a pipe", pw, false},
		{"a regular file", regular, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTerminal(tc.f); got != tc.want {
				t.Fatalf("isTerminal = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a pty", func(t *testing.T) {
		pty, err := openPTY()
		if err != nil {
			t.Skipf("no pty available: %v", err)
		}
		defer pty.Close()
		if !isTerminal(pty) {
			t.Fatal("isTerminal(pty) = false, want true")
		}
	})
}
