package server

import (
	"fmt"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Reader/handler paths only update bounded counters and arm one injected timer.
// They never invoke a potentially blocking loop callback.
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
		if c.diagnosticCount == 0 {
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
		notice := Notice{ConnID: c.id, Level: "warn", Count: c.diagnosticCount, LastCode: c.lastDiagnosticCode, Suppressed: c.diagnosticSuppressed}
		notice.Message = fmt.Sprintf("connection %d: %d diagnostics; last code %s; %d rejections suppressed", c.id, notice.Count, notice.LastCode, notice.Suppressed)
		c.diagnosticCount = 0
		c.diagnosticSuppressed = 0
		c.lastDiagnosticPost = now
		c.hasDiagnosticPost = true
		c.budgetMu.Unlock()
		s.cfg.Post(notice)
	}()
}
