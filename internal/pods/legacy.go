package pods

import (
	"fmt"

	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
)

// LegacyHelloMsg carries a major-1 `hello` seen as the first line of a socket
// connection to the loop. PluginID is hello.plugin_id, "" when it named none.
type LegacyHelloMsg struct{ PluginID string }

// FailLegacy implements the Server's LegacyPeer seam. The connection carried no
// credential, so the plugin is identified by the id its hello named. It is
// worker-safe: it only posts LegacyHelloMsg, which Handle applies on the loop.
func (h *Host) FailLegacy(pluginID string) { h.loop.Post(LegacyHelloMsg{PluginID: pluginID}) }

// failLegacy fails the live Pod named by a socket hello with version_mismatch,
// exactly as a stdout hello does. A Pod that is unknown, not live (starting,
// stopped, failed) or being removed is left alone. Loop-only.
func (h *Host) failLegacy(id string) {
	p := h.pods[id]
	_, removing := h.removing[id]
	if id == "" || p == nil || removing || h.closing || p.info.Status != wrapper.Live || p.process == nil {
		h.loop.Log(LogMsg{PluginID: id, Level: "debug", Message: fmt.Sprintf("major-1 hello on the socket names no live plugin (%q); nothing stopped", id)})
		return
	}
	p.process.FailVersionMismatch()
}
