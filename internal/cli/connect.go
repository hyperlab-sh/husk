package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// EnvPluginToken is the environment variable holding the plugin token; the
// CLI sends it as the token member of every request, whatever instance a flag
// or variable chose.
const EnvPluginToken = "HUSK_PLUGIN_TOKEN"

// Conn is the one connection of a command: open, version-checked and with the
// session.ping reply that checked it.
type Conn struct {
	*Client
	// Ctx bounds the command's requests by --timeout, started when the first
	// byte was sent; it ends when the connection closes.
	Ctx context.Context
	// Ping is the decoded session.ping reply and RawPing its raw result.
	Ping    protocol.SessionPingResult
	RawPing json.RawMessage
}

// Connect resolves the one target, opens one connection to it and sends
// session.ping first: the liveness check of an explicit target and the
// protocol check of every target. A different major, or a missing or
// unparsable version, is exit 6 before anything else is sent. The caller
// sends the command's request on the returned connection and closes it.
func (inv *Invocation) Connect(ctx context.Context) (*Conn, error) {
	res, err := inv.resolve()
	if err != nil {
		return nil, err
	}
	c, err := Dial(ctx, res.Socket, ClientOptions{Token: inv.env.Getenv(EnvPluginToken)})
	if err != nil {
		return nil, inv.dialFailure(err, res)
	}
	cctx, cancel := inv.withTimeout(ctx)
	c.cancel = cancel
	raw, err := c.Call(cctx, protocol.MethodSessionPing, struct{}{})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	var ping protocol.SessionPingResult
	if err := json.Unmarshal(raw, &ping); err != nil {
		_ = c.Close()
		return nil, output.Protocol(err, "unreadable reply: session.ping result: %v", err)
	}
	if err := checkMajor(ping.ProtocolVersion); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &Conn{Client: c, Ctx: cctx, Ping: ping, RawPing: raw}, nil
}

// resolve picks the one target through instance.Resolve. The runtime
// directory is looked up only when the target is an id or discovery: a socket
// path needs no registry. The directory is never created.
func (inv *Invocation) resolve() (instance.Resolved, error) {
	envSocket := inv.env.Getenv(instance.EnvSocketPath)
	opts := instance.ResolveOptions{
		Instance:      inv.instance,
		Socket:        inv.socket,
		EnvSocketPath: envSocket,
		EnvInstance:   inv.env.Getenv(instance.EnvInstance),
		Deps:          inv.env.Deps,
	}
	if inv.socket == "" && (inv.instance != "" || envSocket == "") {
		dir, found, err := inv.env.RuntimeDir()
		if err != nil {
			return instance.Resolved{}, output.Environment(err, "%v", err)
		}
		opts.Dir, opts.DirAbsent, inv.dir = dir, !found, dir
	} else {
		opts.DirAbsent = true // never read: a socket path carries no entry
	}
	res, err := instance.Resolve(opts)
	if err != nil {
		return instance.Resolved{}, resolveErr(err)
	}
	for _, st := range res.Skipped {
		inv.out.Warn(output.WarningUnresponsive,
			fmt.Sprintf("instance %q did not answer within %s; skipped", st.Entry.ID, instance.DiscoveryPingTimeout))
	}
	return res, nil
}

// resolveErr maps a resolution failure to the error object.
func resolveErr(err error) error {
	var ie *instance.Error
	if !errors.As(err, &ie) {
		return output.Transport(err, "%v", err)
	}
	switch ie.Kind {
	case instance.KindUsage:
		return output.Usage("%s", ie.Message)
	case instance.KindNoInstance:
		return output.NoInstance(err, "%s", ie.Message)
	case instance.KindAmbiguous:
		cands := make([]output.Candidate, 0, len(ie.Candidates))
		for _, c := range ie.Candidates {
			cands = append(cands, output.Candidate{ID: c.Entry.ID, PID: c.Entry.PID, StartedAt: c.Entry.StartedAt})
		}
		return output.Ambiguous(ie.Message, cands)
	default:
		return output.Transport(err, "%s", ie.Message)
	}
}

// dialFailure words a failed connect. A refused or missing socket means the
// target went away: for a target found by entry the stale entry is removed
// and said so; a bare socket path has no entry and nothing is removed.
func (inv *Invocation) dialFailure(err error, res instance.Resolved) error {
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitNoInstance {
		return err
	}
	if res.Entry != nil {
		msg := fmt.Sprintf("instance %q is not running", res.Entry.ID)
		// Removal is silent for a target found by discovery; only an
		// explicit target is told its stale entry was removed.
		if instance.RemoveIfStale(inv.dir, *res.Entry) == nil && res.Source != instance.SourceDiscovery {
			msg += " (stale entry removed)"
		}
		return output.NoInstance(err, "%s", msg)
	}
	msg := "nobody is listening on " + res.Socket
	if res.Source == instance.SourceEnv {
		msg += "; unset " + instance.EnvSocketPath + " or pass --instance"
	}
	return output.NoInstance(err, "%s", msg)
}

// checkMajor is the spec's protocol check: a different major is exit 6.
func checkMajor(version string) error {
	major, _, err := protocol.ParseVersion(version)
	if err == nil && major == protocol.Major {
		return nil
	}
	return output.Protocol(err, "instance speaks protocol %s, this CLI speaks %d.x", versionLabel(version), protocol.Major)
}

// versionLabel renders an instance's version as "MAJOR.x" when it parses.
func versionLabel(v string) string {
	if major, _, err := protocol.ParseVersion(v); err == nil {
		return fmt.Sprintf("%d.x", major)
	}
	return fmt.Sprintf("%q", v)
}
