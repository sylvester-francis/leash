// Copyright 2026 Sylvester Francis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
