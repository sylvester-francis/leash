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

// Package term is a stdlib-only helper for TTY-aware ANSI color in leash's
// human output. Color is applied only when writing to a terminal and NO_COLOR
// (https://no-color.org) is unset, so piped and redirected output stays plain.
package term

import (
	"os"
)

// ANSI SGR codes leash uses for status.
const (
	reset = "\x1b[0m"
	green = "\x1b[32m"
	amber = "\x1b[33m"
	red   = "\x1b[31m"
	dim   = "\x1b[2m"
)

// Painter wraps text in ANSI color when enabled, and passes it through
// unchanged otherwise. The zero value is a disabled painter.
type Painter struct{ on bool }

// NewPainter returns a Painter enabled when w is a terminal and NO_COLOR is
// unset. A non-*os.File writer (a buffer, a pipe) yields a disabled painter.
func NewPainter(w any) Painter {
	f, ok := w.(*os.File)
	if !ok {
		return Painter{}
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return Painter{}
	}
	info, err := f.Stat()
	if err != nil {
		return Painter{}
	}
	return Painter{on: info.Mode()&os.ModeCharDevice != 0}
}

// Enabled reports whether the painter colors its output.
func (p Painter) Enabled() bool { return p.on }

func (p Painter) paint(code, s string) string {
	if !p.on {
		return s
	}
	return code + s + reset
}

// Green colors s green (an allowed or running state).
func (p Painter) Green(s string) string { return p.paint(green, s) }

// Amber colors s amber (a boundary-stopped state).
func (p Painter) Amber(s string) string { return p.paint(amber, s) }

// Red colors s red (a killed state).
func (p Painter) Red(s string) string { return p.paint(red, s) }

// Dim renders s dimmed.
func (p Painter) Dim(s string) string { return p.paint(dim, s) }

// Status colors a run status word: running green, stopped amber, killed red,
// anything else unchanged.
func (p Painter) Status(status string) string {
	switch status {
	case "running":
		return p.Green(status)
	case "stopped":
		return p.Amber(status)
	case "killed":
		return p.Red(status)
	default:
		return status
	}
}

// StopReasonColor colors a whole stop line by its boundary reason: a kill is
// red, any other boundary is amber.
func (p Painter) StopReasonColor(line, reason string) string {
	if reason == "kill_switch" {
		return p.Red(line)
	}
	return p.Amber(line)
}
