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

package main

import (
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// runJSON is the stable machine-readable summary of one run, emitted by
// `leash ps --json` (as an array) and embedded in `leash inspect --json`. Field
// names are part of the CLI contract and are documented in cli-reference.md.
type runJSON struct {
	Run             string  `json:"run"`
	Calls           int64   `json:"calls"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	TokenCost       float64 `json:"token_cost"`
	ComputeCost     float64 `json:"compute_cost"`
	TotalCost       float64 `json:"total_cost"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason"`
}

// entryJSON is one journal record in `leash inspect --json`.
type entryJSON struct {
	Seq             int    `json:"seq"`
	Tag             string `json:"tag"`
	At              string `json:"at"`
	Kind            string `json:"kind"`
	Model           string `json:"model,omitempty"`
	InputTokens     int64  `json:"input_tokens,omitempty"`
	OutputTokens    int64  `json:"output_tokens,omitempty"`
	ReasoningTokens int64  `json:"reasoning_tokens,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// inspectJSON is the full machine-readable output of `leash inspect --json`: the
// run summary plus its decoded journal.
type inspectJSON struct {
	runJSON
	Entries []entryJSON `json:"entries"`
}

// toRunJSON projects a folded state into the stable run summary shape.
func toRunJSON(s *policy.State) runJSON {
	return runJSON{
		Run:             s.RunID,
		Calls:           s.Calls,
		InputTokens:     s.InputTokens,
		OutputTokens:    s.OutputTokens,
		ReasoningTokens: s.ReasoningTokens,
		TokenCost:       s.TokenCost,
		ComputeCost:     s.ComputeCost,
		TotalCost:       s.TotalCost,
		Status:          runStatus(s),
		Reason:          s.StopReason,
	}
}

// toEntryJSON projects a decoded journal entry into its stable shape.
func toEntryJSON(e ledger.Entry) entryJSON {
	return entryJSON{
		Seq:             e.Seq,
		Tag:             e.Tag,
		At:              e.At.Format(time.RFC3339),
		Kind:            e.Kind,
		Model:           e.Usage.Model,
		InputTokens:     e.Usage.InputTokens,
		OutputTokens:    e.Usage.OutputTokens,
		ReasoningTokens: e.Usage.ReasoningTokens,
		Reason:          e.Reason,
	}
}
