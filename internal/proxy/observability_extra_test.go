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

package proxy

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestRequestIDHeaderSetAndPropagated(t *testing.T) {
	front, _, _ := buildProxy(t, nil)

	// A request with no id gets a fresh one echoed back.
	resp, err := http.Post(front.URL+chatPath, "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("no X-Request-Id on the response")
	}

	// A valid incoming id is echoed unchanged.
	req, _ := http.NewRequest(http.MethodPost, front.URL+chatPath, strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("X-Request-Id", "trace-abc.123")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "trace-abc.123" {
		t.Fatalf("X-Request-Id = %q, want the propagated trace-abc.123", got)
	}

	// An unsafe incoming id is not reflected; a fresh one is minted.
	req, _ = http.NewRequest(http.MethodPost, front.URL+chatPath, strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("X-Request-Id", "bad id with spaces")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got == "bad id with spaces" || got == "" {
		t.Fatalf("unsafe id was reflected or missing: %q", got)
	}
}

func TestRequestMetricsRecorded(t *testing.T) {
	metrics := NewMetrics("t", nil)
	front, _, _ := buildProxy(t, func(c *Config) { c.Observer = metrics })

	for range 3 {
		resp, err := http.Post(front.URL+chatPath, "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
	}

	var out bytes.Buffer
	metrics.WriteTo(&out, 0)
	s := out.String()
	for _, want := range []string{
		"leash_requests_in_flight 0",
		`leash_responses_total{code="200"} 3`,
		"leash_request_duration_seconds_count 3",
		`leash_request_duration_seconds_bucket{le="+Inf"} 3`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("metrics missing %q:\n%s", want, s)
		}
	}
}
