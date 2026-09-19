// testing.go: test-only helpers that must be exported for cross-package
// injection (hub tests drive the recovery path). Unreachable in normal play;
// keep this file free of production logic.
package game

// Poison forces the error phase (test-only).
func (g *Game) Poison() { g.phase = PhaseError }
