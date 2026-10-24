package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

// fakeUpstream scrubs every inherited upstream variable before selecting the
// fixture socket. Children receive the same allowlist outside the HUSK namespace.
func fakeUpstream(t *testing.T, socket string, options fakeherdr.Options) *fakeherdr.Server {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "HERDR_") {
			t.Setenv(key, "")
		}
	}
	server, err := fakeherdr.Start(socket, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Setenv("ADAPTER_TEST_UPSTREAM_SOCKET", socket)
	return server
}

func guardedDial(allowed string, fail func(string)) upstream.Dialer {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if allowed == "" || network != "unix" || address != allowed {
			message := fmt.Sprintf("refusing non-fixture upstream dial: %s %q (allowed %q)", network, address, allowed)
			fail(message)
			return nil, fmt.Errorf("%s", message)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}

func guardUpstream(t *testing.T, env environment) environment {
	t.Helper()
	socket := os.Getenv("ADAPTER_TEST_UPSTREAM_SOCKET")
	if socket == "" {
		t.Fatal("test must select fakeUpstream before runtime execution")
	}
	env.upstreamDial = guardedDial(socket, func(message string) { t.Error(message) })
	return env
}

// childEnvironment is the only runtime entry used by re-executed test plugins.
// Fail closed even if a test forgets to choose a fixture or settings override it.
func childEnvironment(exe string) environment {
	env := captureEnvironment(exe, runtime.GOOS, os.Getenv, os.UserHomeDir, os.LookupEnv)
	env.upstreamDial = guardedDial(os.Getenv("ADAPTER_TEST_UPSTREAM_SOCKET"), func(message string) {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(97)
	})
	return env
}

func TestUpstreamGuardRejectsNonFixtureBeforeDial(t *testing.T) {
	rejected := false
	dial := guardedDial("/fixture.sock", func(string) { rejected = true })
	if conn, err := dial(t.Context(), "unix", "/not-fixture.sock"); conn != nil || err == nil || !rejected {
		t.Fatalf("unsafe dial accepted: conn=%v err=%v rejected=%t", conn, err, rejected)
	}
}
