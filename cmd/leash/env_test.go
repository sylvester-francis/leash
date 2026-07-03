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
	"flag"
	"testing"
)

// parseCommon registers the shared flags on a throwaway flag set and parses
// args, returning the populated commonFlags for assertions.
func parseCommon(t *testing.T, args []string) *commonFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := registerCommon(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return c
}

func TestEnvFallbackPrecedence(t *testing.T) {
	// Default wins when neither flag nor env is set.
	if c := parseCommon(t, nil); c.maxCost != 5.00 {
		t.Fatalf("default max-cost = %v, want 5.00", c.maxCost)
	}

	// Env beats default.
	t.Setenv("LEASH_MAX_COST", "99")
	t.Setenv("LEASH_DB", "/tmp/from-env.db")
	if c := parseCommon(t, nil); c.maxCost != 99 {
		t.Fatalf("env max-cost = %v, want 99", c.maxCost)
	}
	if c := parseCommon(t, nil); c.db != "/tmp/from-env.db" {
		t.Fatalf("env db = %q, want /tmp/from-env.db", c.db)
	}

	// Explicit flag beats env.
	if c := parseCommon(t, []string{"--max-cost", "7"}); c.maxCost != 7 {
		t.Fatalf("flag max-cost = %v, want 7 (flag beats env)", c.maxCost)
	}
}

func TestEnvFallbackTypedAndBool(t *testing.T) {
	t.Setenv("LEASH_MAX_CALLS", "42")
	t.Setenv("LEASH_DEADLINE", "90s")
	t.Setenv("LEASH_NO_INJECT", "true")
	c := parseCommon(t, nil)
	if c.maxCalls != 42 {
		t.Fatalf("env max-calls = %d, want 42", c.maxCalls)
	}
	if c.deadline.String() != "1m30s" {
		t.Fatalf("env deadline = %v, want 1m30s", c.deadline)
	}
	if !c.noInject {
		t.Fatalf("env no-inject = false, want true")
	}
}

func TestEnvInvalidValueFallsBackToDefault(t *testing.T) {
	t.Setenv("LEASH_MAX_COST", "not-a-number")
	if c := parseCommon(t, nil); c.maxCost != 5.00 {
		t.Fatalf("invalid env max-cost gave %v, want the 5.00 default", c.maxCost)
	}
}
