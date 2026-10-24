package shim

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestStartLockReprobesBeforeLaunchingOneServer(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.server.Close()
	o := r.o
	o.Env = append(o.Env, "XDG_STATE_HOME="+t.TempDir())
	o.Settings.Session = "named"
	o.Client = upstream.New(upstream.Options{Socket: o.Socket, Clock: o.Clock, Dial: guardedDial(t, o.Socket)})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var launches atomic.Int32
	var server *fakeherdr.Server
	o.ServerLaunch = func(binary string, args, env []string) error {
		n := launches.Add(1)
		if n != 1 {
			return nil
		}
		if binary != o.Binary || !reflect.DeepEqual(args, []string{"--session", "named", "server"}) {
			t.Errorf("server argv=%s %v", binary, args)
		}
		for _, v := range env {
			key, _, _ := strings.Cut(v, "=")
			if strings.HasPrefix(key, "HERDR_PLUGIN_") || key == "HUSK_PLUGIN_TOKEN" {
				t.Errorf("server inherited private field %s", key)
			}
		}
		close(entered)
		<-release
		var err error
		server, err = fakeherdr.Start(o.Socket, fakeherdr.Options{})
		return err
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- startServer(context.Background(), o) }()
	take(t, entered)
	go func() { second <- startServer(context.Background(), o) }()
	r.clock.fire(t, 100*time.Millisecond)
	if got := launches.Load(); got != 1 {
		t.Fatalf("lock holder count=%d", got)
	}
	once.Do(func() { close(release) })
	if err := take(t, first); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	r.clock.fire(t, 100*time.Millisecond)
	if err := take(t, second); err != nil {
		t.Fatal(err)
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("server started %d times", got)
	}
}
