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

import (
	"strings"
	"testing"
)

func TestValidRunID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"simple", "a3f9", true},
		{"alnum", "run123", true},
		{"dots dashes underscores", "my.run_id-2", true},
		{"single char", "x", true},
		{"max length 118", "a" + strings.Repeat("b", 117), true},
		{"empty", "", false},
		{"too long 119", "a" + strings.Repeat("b", 118), false},
		{"leading dot", ".hidden", false},
		{"leading dash", "-flag", false},
		{"leading underscore", "_x", false},
		{"newline (log injection)", "run\nid", false},
		{"embedded space", "run id", false},
		{"unicode", "run\u00e9", false},
		{"path traversal", "../etc/passwd", false},
		{"slash", "a/b", false},
		{"tab", "a\tb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidRunID(tt.id); got != tt.want {
				t.Fatalf("ValidRunID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
