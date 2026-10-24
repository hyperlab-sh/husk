package shim

import (
	"encoding/json"
	"regexp"
	"strings"

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

type compiledLinkHandler struct {
	entry   manifest.Entry
	action  manifest.Entry
	pattern *regexp.Regexp
}

// compileLinkHandlers follows find_plugin_link_handler in upstream
// plugins/mod.rs at 5608de0608918aba2f9d501e7793b984a0ca9cfa: retain manifest
// order, require handler/action platform eligibility, and skip invalid patterns
// or missing actions. Compile once for the runner's immutable manifest.
func compileLinkHandlers(m manifest.Manifest, platform string) []compiledLinkHandler {
	var handlers []compiledLinkHandler
	for _, handler := range m.LinkHandlers {
		if !manifest.Supports(handler.Platforms, platform) {
			continue
		}
		pattern, err := regexp.Compile(handler.Pattern)
		if err != nil {
			continue
		}
		for _, action := range m.Actions {
			if action.ID == handler.Action && manifest.Supports(action.Platforms, platform) {
				handlers = append(handlers, compiledLinkHandler{entry: handler, action: action, pattern: pattern})
				break
			}
		}
	}
	return handlers
}

// Matching is a substring search unless anchored and case-sensitive unless the
// pattern opts in. Go's Perl classes and word boundaries are ASCII, unlike
// upstream Rust's Unicode defaults; this divergence is documented in Limits.
func (r *runner) handleLink(target string) {
	for _, handler := range r.links {
		if !handler.pattern.MatchString(target) {
			continue
		}
		context, _ := json.Marshal(InvocationContext{ClickedURL: target, LinkHandlerID: handler.entry.ID})
		if _, err := r.launch(handler.action, handler.action.ID, "", nil, "link_click", "link_click", context); err != nil {
			r.logLine(map[string]any{"level": "warning", "link_handler_id": handler.entry.ID, "error": err.Error()})
		}
		return
	}
	// Match safe_web_url in upstream src/app/actions.rs at the pinned revision:
	// lowercase literal prefixes only, not normalized schemes or opaque URLs.
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		r.logLine(map[string]any{"level": "debug", "message": "unmatched terminal link ignored", "url": target})
		return
	}
	opener := "xdg-open"
	if r.o.Platform == "darwin" {
		opener = "open"
	}
	command := Command{Argv: []string{opener, target}, Env: openerEnvironment(r.o.Env), Dir: r.o.Dir}
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
