package calls

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// SweepCmd is the thin Bubble Tea scheduling adapter. It produces one tick only
// when NextSweep can claim the sweep slot. The loop handles its SweepMsg with
// Sweep, delivers results in the same Update, then batches SweepCmd again.
func (t *Table) SweepCmd() tea.Cmd { return sweepCommand(t, tea.Tick) }

// Passing the tick factory here keeps scheduling out of Table and Clock, and
// lets tests exercise the adapter without creating timers or running a program.
func sweepCommand(t *Table, tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd) tea.Cmd {
	delay, ok := t.NextSweep()
	if !ok {
		return nil
	}
	return tick(delay, func(time.Time) tea.Msg { return SweepMsg{} })
}
