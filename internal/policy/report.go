package policy

import "fmt"

// StopLine formats the single human-readable line leash prints when a run
// stops, for example:
//
//	leash: stopped run a3f9 after 18 calls, $4.10 tokens + $0.91 compute = $5.01 (cost_budget)
//
// Costs are shown to the cent and the trailing parenthesis names the boundary.
func StopLine(s *State) string {
	return fmt.Sprintf(
		"leash: stopped run %s after %d calls, $%.2f tokens + $%.2f compute = $%.2f (%s)",
		s.RunID, s.Calls, s.TokenCost, s.ComputeCost, s.TotalCost, s.StopReason,
	)
}
