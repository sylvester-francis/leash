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
