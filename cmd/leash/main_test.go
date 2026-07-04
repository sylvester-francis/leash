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
	"encoding/hex"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	tests := []struct {
		in      string
		tokens  int64
		window  time.Duration
		wantErr bool
	}{
		{"", 0, 0, false},
		{"100000/1m", 100000, time.Minute, false},
		{"500/30s", 500, 30 * time.Second, false},
		{"missing-slash", 0, 0, true},
		{"100/", 0, 0, true},
		{"abc/1m", 0, 0, true},
		{"100/xyz", 0, 0, true},
		{"100/0s", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			tok, win, err := parseRate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRate(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && (tok != tt.tokens || win != tt.window) {
				t.Fatalf("parseRate(%q) = (%d,%v), want (%d,%v)", tt.in, tok, win, tt.tokens, tt.window)
			}
		})
	}
}

func TestParseUpstream(t *testing.T) {
	if u, err := parseUpstream(""); u != nil || err != nil {
		t.Fatalf("empty upstream should be (nil,nil), got (%v,%v)", u, err)
	}
	u, err := parseUpstream("http://gateway:8080")
	if err != nil || u.Host != "gateway:8080" {
		t.Fatalf("valid upstream failed: u=%v err=%v", u, err)
	}
	if _, err := parseUpstream("notaurl"); err == nil {
		t.Fatalf("a bare word without scheme/host should be rejected")
	}
	if _, err := parseUpstream("://bad"); err == nil {
		t.Fatalf("an unparseable url should be rejected")
	}
}

func TestShortIDIsFourHexChars(t *testing.T) {
	id := shortID()
	if len(id) != 4 {
		t.Fatalf("shortID() = %q, want 4 characters", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("shortID() = %q, want hex: %v", id, err)
	}
}

func TestDefaultDBPathEndsInLeashDB(t *testing.T) {
	p := defaultDBPath()
	if p == "" {
		t.Fatalf("defaultDBPath is empty")
	}
	if filepathBase(p) != "leash.db" {
		t.Fatalf("defaultDBPath = %q, want it to end in leash.db", p)
	}
}

// filepathBase avoids importing path/filepath just for the test assertion.
func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
