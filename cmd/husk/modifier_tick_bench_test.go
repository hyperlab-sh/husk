package main

import "testing"

// BenchmarkIdleTickView measures what one idle modifier tick costs the
// process: the OS modifier poll plus the View rebuild Bubble Tea performs
// after every Update. Multiply ns/op by the idle rate (1s / modifierTickIdle)
// for the steady-state idle CPU share.
func BenchmarkIdleTickView(b *testing.B) {
	m := newWorkbenchModel("", nil, b.TempDir())
	m.width, m.height = 120, 40
	m.recomputeLayout()
	b.Run("poll", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			m.pollMods()
		}
	})
	b.Run("view", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = m.View()
		}
	})
	b.Run("tick_update_and_view", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			m.Update(modifierTickMsg{})
			_ = m.View()
		}
	})
}
