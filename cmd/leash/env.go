package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Environment fallbacks. Every shared flag reads a LEASH_-prefixed variable
// named mechanically from the flag: --max-cost reads LEASH_MAX_COST, --db reads
// LEASH_DB, and so on. Precedence is explicit flag, then environment, then the
// built-in default. This is implemented by using the environment value (when
// set) as the flag's default, so an explicit flag on the command line still
// wins. A malformed environment value is reported and the built-in default is
// used, rather than failing the whole command on a stray variable.

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
