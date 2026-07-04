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
	"net"
	"net/http"
	"time"
)

// Server hardening timeouts. No WriteTimeout or whole-request deadline: a
// governed SSE stream can run for minutes and must not be severed.
const (
	serverReadHeaderTimeout = 10 * time.Second // defeats a slow-header hold
	serverIdleTimeout       = time.Minute      // bounds idle keep-alive
	serverMaxHeaderBytes    = 64 * 1024
)

// Upstream transport hardening.
const (
	dialTimeout         = 10 * time.Second
	dialKeepAlive       = 30 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	maxIdleConnsPerHost = 31
)

// HardenedServer builds an http.Server with leash's request-hardening timeouts.
// Both the standalone gateway and the embedded wrapper server go through here so
// they cannot drift. WriteTimeout stays unset so SSE streams are not severed.
func HardenedServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

// newUpstreamClient builds the upstream HTTP client. It bounds dial and TLS
// setup and, when non-zero, the response-header wait (so a slow reasoning model
// is tolerated but a dead upstream is not). No overall client timeout: once
// headers arrive the streamed body may run as long as it needs.
func newUpstreamClient(headerTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: dialKeepAlive,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			MaxIdleConnsPerHost:   maxIdleConnsPerHost,
			ResponseHeaderTimeout: headerTimeout,
		},
	}
}
