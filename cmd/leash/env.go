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
	"fmt"
	"os"
	"strconv"
	"time"
)

// Environment fallbacks. Every shared flag reads a LEASH_-prefixed variable
// (--max-cost reads LEASH_MAX_COST, and so on). Precedence is flag, then env,
// then default: the env value becomes the flag default, so an explicit flag
// still wins. A malformed value is reported and the default used.

// envStr returns the environment value for name, or def when it is unset.
func envStr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

// envFloat parses a float from the environment, falling back to def.
func envFloat(name string, def float64) float64 {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		warnBadEnv(name, v, err)
		return def
	}
	return f
}

// envInt64 parses an int64 from the environment, falling back to def.
func envInt64(name string, def int64) int64 {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		warnBadEnv(name, v, err)
		return def
	}
	return n
}

// envInt parses an int from the environment, falling back to def.
func envInt(name string, def int) int {
	return int(envInt64(name, int64(def)))
}

// envDuration parses a Go duration from the environment, falling back to def.
func envDuration(name string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		warnBadEnv(name, v, err)
		return def
	}
	return d
}

// envBool parses a bool from the environment, falling back to def.
func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		warnBadEnv(name, v, err)
		return def
	}
	return b
}

// warnBadEnv reports a malformed environment value without failing the command.
func warnBadEnv(name, value string, err error) {
	fmt.Fprintf(os.Stderr, "leash: ignoring invalid %s=%q: %v\n", name, value, err)
}
