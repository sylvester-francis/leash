package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// buildLogger constructs a structured logger from the --log-level and
// --log-format flags. leash logs only redacted operational facts: run ids,
// decisions, counts, and errors. It never logs a header value or a body, so no
// log level ever reveals a secret.
func buildLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid --log-level %q (want debug, info, warn, or error)", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "text":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, fmt.Errorf("invalid --log-format %q (want text or json)", format)
	}
	return slog.New(h), nil
}
