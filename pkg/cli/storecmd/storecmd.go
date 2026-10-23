// Package storecmd is the `husk store` noun: local commands that open Store
// without an instance (docs/docs/design/instances.md, rule 9 of the command
// table). It is the only command package that imports Store. It never imports
// pkg/cli and never talks to an instance; its one socket use is the
// probes Survey makes before reset-secrets. cmd/husk wires it with
// cli.Register("store", storecmd.Command()).
package storecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// closeBound bounds Store.Close; nothing was written, so it returns at once.
const closeBound = 2 * time.Second

// deps are the seams tests replace; the zero value of a field is production.
type deps struct {
	// runtimeDir looks the runtime directory up without creating it.
	runtimeDir func() (dir string, found bool, err error)
	// env, keyring and instance feed store.Options.
	env      store.Env
	keyring  store.Keyring
	instance *instance.Deps
}

// Command is the `store` noun: paths, check, gc and reset-secrets.
func Command() output.Command { return command(deps{runtimeDir: instance.LookupRuntimeDir}) }

func command(d deps) output.Command {
	return output.Command{
		Noun:  "store",
		Usage: "husk store paths | check | gc | reset-secrets",
		Run: func(ctx context.Context, args []string, o output.Options) error {
			return run(ctx, d, args, o)
		},
	}
}

func run(ctx context.Context, d deps, args []string, o output.Options) error {
	if len(args) == 0 {
		return output.Usage("store takes a verb: paths, check, gc or reset-secrets")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "paths", "check", "gc", "reset-secrets":
	default:
		return output.Usage("unknown store verb %q: use paths, check, gc or reset-secrets", verb)
	}
	if len(rest) != 0 {
		return output.Usage("store %s takes no arguments", verb)
	}
	dir, found, err := d.runtimeDir()
	if err != nil {
		return output.Environment(err, "%v", err)
	}
	if !found {
		dir = ""
	}
	if verb == "reset-secrets" {
		if err := refuseWhileRunning(dir, d, o); err != nil {
			return err
		}
	}
	// Passive for every verb: Open itself must not collect, quarantine or write
	// metadata, so check moves nothing, gc's report is the whole truth and a
	// reset touches only secrets. Explicit GC and ResetSecrets still act.
	s, err := store.Open(store.Options{RuntimeDir: dir, Env: d.env, Keyring: d.keyring, Passive: true, InstanceDeps: d.instance})
	if err != nil {
		return output.Store(err, "%v", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeBound)
		defer cancel()
		_ = s.Close(cctx)
	}()

	switch verb {
	case "paths":
		p := s.Paths()
		return result(o, map[string]string{"config": p.Config, "state": p.State, "data": p.Data, "runtime": p.Runtime})
	case "check":
		r, err := s.Check(ctx)
		if err != nil {
			return output.Store(err, "%v", err)
		}
		return result(o, map[string]any{"problems": nonNil(r.Problems), "quarantined": nonNil(r.Quarantined), "orphans": nonNil(r.Orphans)})
	case "gc":
		r, err := s.GC(ctx)
		if err != nil {
			return output.Store(err, "%v", err)
		}
		return result(o, map[string]any{"removed": nonNil(r.Removed), "problems": nonNil(r.Problems)})
	default: // reset-secrets
		if err := s.ResetSecrets(); err != nil {
			return output.Store(err, "%v", err)
		}
		return result(o, map[string]any{"reset": true})
	}
}

// refuseWhileRunning is exit 1 `store` naming every live or unresponsive
// instance: a reset under a running instance would strand its open secrets.
// An absent runtime directory means nothing runs. The check is a point in
// time: an instance that starts between this survey and ResetSecrets is not
// seen; the spec asks for the refusal only.
func refuseWhileRunning(dir string, d deps, o output.Options) error {
	if dir == "" {
		return nil
	}
	statuses, err := instance.Survey(dir, d.instance)
	if err != nil {
		return output.Environment(err, "read runtime dir: %v", err)
	}
	var ids []string
	for _, st := range statuses {
		if st.Verdict == instance.VerdictLive || st.Verdict == instance.VerdictUnresponsive {
			ids = append(ids, st.Entry.ID)
		}
	}
	switch len(ids) {
	case 0:
		return nil
	case 1:
		return output.Store(nil, "1 instance is registered (%s); stop it first", ids[0])
	}
	return output.Store(nil, "%d instances are registered (%s); stop them first", len(ids), strings.Join(ids, ", "))
}

// result prints v as the command's one document in the chosen format.
func result(o output.Options, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return output.Store(err, "%v", err)
	}
	if err := o.Result(raw); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// nonNil keeps an empty list a JSON [] instead of null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
