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
	"net/http"
	"testing"
	"time"
)

func TestHardenedServerSetsTimeouts(t *testing.T) {
	srv := HardenedServer(":8088", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != time.Minute {
		t.Fatalf("IdleTimeout = %v, want 1m", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 64*1024 {
		t.Fatalf("MaxHeaderBytes = %d, want 65536", srv.MaxHeaderBytes)
	}
	// WriteTimeout must stay zero so long SSE streams are never severed.
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 (streams must not be cut off)", srv.WriteTimeout)
	}
	if srv.Addr != ":8088" {
		t.Fatalf("Addr = %q, want :8088", srv.Addr)
	}
}

func TestUpstreamClientSetsTransportTimeouts(t *testing.T) {
	c := newUpstreamClient(5 * time.Minute)
	if c.Timeout != 0 {
		t.Fatalf("client Timeout = %v, want 0 (no overall timeout for streams)", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.MaxIdleConnsPerHost != 31 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 31", tr.MaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout != 5*time.Minute {
		t.Fatalf("ResponseHeaderTimeout = %v, want 5m", tr.ResponseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Fatalf("DialContext is nil, want a dialer with a 10s timeout")
	}
}

func TestUpstreamClientZeroHeaderTimeoutDisables(t *testing.T) {
	tr := newUpstreamClient(0).Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %v, want 0 (disabled)", tr.ResponseHeaderTimeout)
	}
}
