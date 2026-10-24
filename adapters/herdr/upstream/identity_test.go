package upstream_test

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestAttachObservesPingIdentityBeforePluginList(t *testing.T) {
	path := socket(t)
	var observed atomic.Bool
	s := server(t, path, fakeherdr.Options{OnRequest: func(req fakeherdr.Request) {
		if req.Method == "plugin.list" && !observed.Load() {
			t.Error("plugin.list preceded ping identity capture")
		}
	}})
	session, err := client(path, newClock()).Attach(t.Context(), "0.9.0", func(pong upstream.Pong) error {
		if len(s.Requests()) != 1 || pong.Version != "0.9.5" || pong.Protocol != 22 {
			t.Errorf("wrong ping boundary or fields: %+v", pong)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		if stat.Ino == 0 {
			t.Error("socket identity was not captured")
		}
		observed.Store(true)
		return nil
	})
	if err != nil || !observed.Load() || session.Protocol != 22 || len(s.Requests()) != 2 {
		t.Fatalf("identity observer/session: %+v %v", session, err)
	}
}

func TestNothingListeningRequiresMissingOrRefusedDial(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		cause    error
		absent   bool
	}{
		{"missing", "dial", syscall.ENOENT, true},
		{"refused", "dial", syscall.ECONNREFUSED, true},
		{"read_missing", "read", syscall.ENOENT, false},
		{"permission", "dial", syscall.EACCES, false},
		{"other", "dial", errors.New("other transport failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := upstream.New(upstream.Options{Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: tc.op, Err: tc.cause}
			}})
			_, err := c.Dial(t.Context())
			named(t, err, "unavailable")
			if !errors.Is(err, tc.cause) || upstream.NothingListening(err) != tc.absent || upstream.IsRemote(err) {
				t.Fatalf("dial cause/classification lost: %v", err)
			}
		})
	}
}
