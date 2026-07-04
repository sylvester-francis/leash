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

package term

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plain reports whether the painter leaves text unchanged (color disabled).
func plain(p Painter) bool { return p.Green("x") == "x" }

func TestNewPainterDisabledForNonTTY(t *testing.T) {
	if !plain(NewPainter(&bytes.Buffer{})) {
		t.Fatalf("painter colored a bytes.Buffer, want plain")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()
	if !plain(NewPainter(f)) {
		t.Fatalf("painter colored a regular file, want plain")
	}
}

func TestNoColorDisables(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Even for stdout, NO_COLOR forces plain.
	if !plain(NewPainter(os.Stdout)) {
		t.Fatalf("painter colored with NO_COLOR set, want plain")
	}
}

func TestPaintPassthroughWhenOff(t *testing.T) {
	var p Painter // zero value: off
	for _, got := range []string{p.Green("x"), p.Amber("x"), p.Red("x"), p.Status("running"), p.StopReasonColor("l", "kill_switch")} {
		if strings.Contains(got, "\x1b") {
			t.Fatalf("disabled painter emitted an escape code: %q", got)
		}
	}
}

func TestPaintWrapsWhenOn(t *testing.T) {
	p := Painter{on: true}
	if !strings.HasPrefix(p.Green("x"), "\x1b[32m") || !strings.HasSuffix(p.Green("x"), "\x1b[0m") {
		t.Fatalf("Green did not wrap: %q", p.Green("x"))
	}
	if p.Status("stopped") != p.Amber("stopped") {
		t.Fatalf("stopped status should be amber")
	}
	if p.Status("killed") != p.Red("killed") {
		t.Fatalf("killed status should be red")
	}
	if p.Status("running") != p.Green("running") {
		t.Fatalf("running status should be green")
	}
	// A kill stop line is red; other reasons amber.
	if p.StopReasonColor("line", "kill_switch") != p.Red("line") {
		t.Fatalf("kill stop line should be red")
	}
	if p.StopReasonColor("line", "cost_budget") != p.Amber("line") {
		t.Fatalf("non-kill stop line should be amber")
	}
}
