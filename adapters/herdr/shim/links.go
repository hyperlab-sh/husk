package shim

import (
	"encoding/json"
	"net/url"
	"regexp"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// linkNotification admits only this shim's live terminal lifetimes. Context
// queries, command startup and the opener must never block the stdin reader.
func (r *runner) linkNotification(raw json.RawMessage) {
	var link protocol.TerminalLinkParams
	if json.Unmarshal(raw, &link) != nil || link.URL == "" {
		return
	}
	r.mu.Lock()
	p := r.panes[link.Surface]
	r.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	live := !p.closed && p.process != nil
	p.mu.Unlock()
	if !live {
		return
	}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		r.handleLink(link.URL)
	}()
}

// Matching follows find_plugin_link_handler in upstream plugins/mod.rs at
// 5608de0608918aba2f9d501e7793b984a0ca9cfa: manifest order, regex is_match
// (substring unless anchored), case-sensitive unless the pattern opts in, and
// both handler and action must support this platform. Unsupported RE2 patterns
// and missing actions are skipped just as at conversion.
func (r *runner) handleLink(target string) {
	for _, handler := range r.m.LinkHandlers {
		if !manifest.Supports(handler.Platforms, r.o.Platform) {
			continue
		}
		pattern, err := regexp.Compile(handler.Pattern)
		if err != nil {
			continue
		}
		for _, action := range r.m.Actions {
			if action.ID != handler.Action || !manifest.Supports(action.Platforms, r.o.Platform) {
				continue
			}
			if !pattern.MatchString(target) {
				break
			}
			context, _ := json.Marshal(InvocationContext{ClickedURL: target, LinkHandlerID: handler.ID})
			if _, err := r.launch(action, action.ID, "", nil, "link_click", "link_click", context); err != nil {
				r.logLine(map[string]any{"level": "warning", "link_handler_id": handler.ID, "error": err.Error()})
			}
			return
		}
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		r.logLine(map[string]any{"level": "debug", "message": "unmatched terminal link ignored", "url": target})
		return
	}
	opener := "xdg-open"
	if r.o.Platform == "darwin" {
		opener = "open"
	}
	command := Command{Argv: []string{opener, target}, Env: CommandEnvironment(r.o.Env, r.o.Dir, r.o.Binary, r.proxy.Path(), r.o.ConfigDir, r.o.StateDir, r.m.ID, "", "", nil, InvocationContext{}), Dir: r.o.Dir}
	process, err := r.o.SpawnOpener(command)
	if err != nil {
		r.logLine(map[string]any{"command": command.Argv, "error": err.Error()})
		return
	}
	// Reap and log independently. A browser can outlive the shim; neither stdin
	// delivery nor shutdown joins it or signals its process group.
	go func() {
		status, stderr := process.Wait()
		r.logLine(map[string]any{"command": command.Argv, "status": status, "stderr": stderr})
	}()
}
