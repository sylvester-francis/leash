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
	"io"
	"net/http"
	"strings"
)

// flushWriter forwards writes to the client and flushes after each one, so a
// streamed SSE response reaches the client chunk by chunk instead of buffering.
type flushWriter struct {
	w  io.Writer
	fl http.Flusher
}

// newFlushWriter wraps w, using its Flusher when it has one.
func newFlushWriter(w http.ResponseWriter) *flushWriter {
	fw := &flushWriter{w: w}
	if fl, ok := w.(http.Flusher); ok {
		fw.fl = fl
	}
	return fw
}

// Write writes to the client and flushes.
func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.fl != nil {
		fw.fl.Flush()
	}
	return n, err
}

// hopByHopHeaders are connection-scoped headers that must not be forwarded
// between the client and the upstream.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// leashInternalHeaders are leash's own routing headers; they are consumed here
// and not sent upstream.
var leashInternalHeaders = map[string]bool{
	"x-loop-id": true,
}

// copyRequestHeader copies client request headers to the upstream request. It
// forwards Authorization, x-api-key, and every other header untouched so the
// upstream authenticates normally; it drops hop-by-hop, leash-internal, and
// Content-Length headers (the latter is set fresh from the rewritten body).
func copyRequestHeader(dst, src http.Header) {
	for name, values := range src {
		lower := strings.ToLower(name)
		if hopByHopHeaders[lower] || leashInternalHeaders[lower] || lower == "content-length" {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// copyHeader copies response headers to the client, dropping hop-by-hop headers.
func copyHeader(dst, src http.Header) {
	for name, values := range src {
		if hopByHopHeaders[strings.ToLower(name)] {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}
