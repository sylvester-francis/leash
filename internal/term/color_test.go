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

func TestNewPainterDisabledForNonTTY(t *testing.T) {
	if NewPainter(&bytes.Buffer{}).Enabled() {
		t.Fatalf("painter enabled for a bytes.Buffer, want disabled")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()
	if NewPainter(f).Enabled() {
		t.Fatalf("painter enabled for a regular file, want disabled")
	}
}

func TestNoColorDisables(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Even for stdout, NO_COLOR forces plain.
	if NewPainter(os.Stdout).Enabled() {
		t.Fatalf("painter enabled with NO_COLOR set, want disabled")
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
