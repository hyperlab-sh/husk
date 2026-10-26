package server

import (
	"fmt"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// maxIgnoredMethod caps the peer-supplied method kept for the debug notice.
const maxIgnoredMethod = 128

// Reader/handler paths only update bounded counters and arm one injected timer.
// They never invoke a potentially blocking loop callback. Each interval posts
// up to two notices per connection (warn diagnostics and debug ignored
// notifications), in order, from the single posting goroutine.
func (c *connection) noteDiagnostic(code protocol.Code) {
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	c.noteDiagnosticLocked(code, false)
}
func (c *connection) noteDiagnosticLocked(code protocol.Code, suppressed bool) {
	c.diagnosticCount++
	c.lastDiagnosticCode = code
	if suppressed {
		c.diagnosticSuppressed++
	}
	c.armDiagnosticLocked()
}

// noteIgnored counts a notification dropped because its method is unknown
// (protocol.md: no response, logged at debug level). It shares the coalescing
// timer and post budget of the warn diagnostics but reports as its own debug
// notice, so a flood of them costs at most one loop message per interval. The
// method is peer-controlled, so it is truncated before it is kept.
func (c *connection) noteIgnored(method string) {
	if len(method) > maxIgnoredMethod {
		// protocol.ValidateMethod admits only ASCII, so this cuts no rune.
		method = method[:maxIgnoredMethod]
	}
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	c.ignoredCount++
	c.lastIgnoredMethod = method
	c.armDiagnosticLocked()
}

func (c *connection) armDiagnosticLocked() {
	if c.diagnosticTimer == nil && c.ctx.Err() == nil && c.server.cfg.Post != nil {
		c.diagnosticTimer = c.server.cfg.Clock.AfterFunc(DiagnosticInterval, c.publishDiagnostic)
	}
}

func (c *connection) publishDiagnostic() {
	c.budgetMu.Lock()
	c.diagnosticTimer = nil
	c.budgetMu.Unlock()
	s := c.server
	if s.cfg.Post == nil || !s.diagnosticPosting.CompareAndSwap(false, true) {
		return
	}
	// One outstanding diagnostic callback across this server, not one goroutine
	// per rejection or abandoned connection. A behind loop drops later offers.
	go func() {
		defer s.diagnosticPosting.Store(false)
		c.budgetMu.Lock()
		now := s.cfg.Clock.Now()
		if c.diagnosticCount == 0 && c.ignoredCount == 0 {
			c.budgetMu.Unlock()
			return
		}
		if elapsed := now.Sub(c.lastDiagnosticPost); c.hasDiagnosticPost && elapsed < DiagnosticInterval {
			if c.diagnosticTimer == nil && c.ctx.Err() == nil {
				c.diagnosticTimer = s.cfg.Clock.AfterFunc(DiagnosticInterval-elapsed, c.publishDiagnostic)
			}
			c.budgetMu.Unlock()
			return
		}
		var notices []Notice
		if c.diagnosticCount > 0 {
			notice := Notice{ConnID: c.id, Level: "warn", Count: c.diagnosticCount, LastCode: c.lastDiagnosticCode, Suppressed: c.diagnosticSuppressed}
			notice.Message = fmt.Sprintf("connection %d: %d diagnostics; last code %s; %d rejections suppressed", c.id, notice.Count, notice.LastCode, notice.Suppressed)
			notices = append(notices, notice)
		}
		if c.ignoredCount > 0 {
			notice := Notice{ConnID: c.id, Level: "debug", Count: c.ignoredCount, LastCode: protocol.CodeUnknownMethod}
			notice.Message = fmt.Sprintf("connection %d: %d notifications for unknown methods ignored; last method %q", c.id, notice.Count, c.lastIgnoredMethod)
			notices = append(notices, notice)
		}
		c.diagnosticCount = 0
		c.diagnosticSuppressed = 0
		c.ignoredCount = 0
		c.lastDiagnosticPost = now
		c.hasDiagnosticPost = true
		c.budgetMu.Unlock()
		for _, n := range notices {
			s.cfg.Post(n)
		}
	}()
}
