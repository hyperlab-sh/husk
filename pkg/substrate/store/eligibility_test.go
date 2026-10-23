package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

func TestNonPersistentAndAnonymousWorkspaceNeverWritten(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "4242", nil)
	st := attach(t, s)
	ephemeral, err := st.Register(state.Spec{Namespace: "plugin.ephemeral", Owner: "plugin", Scope: state.ScopeUser})
	if err != nil {
		t.Fatal(err)
	}
	set(t, ephemeral, `{"transient":true}`)
	workspace, err := st.Register(state.Spec{Namespace: "plugin.workspace", Owner: "plugin", Scope: state.ScopeWorkspace, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}})
	if err != nil {
		t.Fatal(err)
	}
	set(t, workspace, `{"workspace":true}`)
	st.Flush()
	if err = s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.source.Get(context.Background(), Key{Scope: state.ScopeUser, Path: []string{"slices", "plugin.ephemeral", "current.json"}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("non-persistent slice written", err)
	}
	if _, err = os.Stat(filepath.Join(s.paths.State, "instances", "4242")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("anonymous workspace written", err)
	}
}
