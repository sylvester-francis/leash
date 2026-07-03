package proxy

import (
	"net"
	"net/http"
	"time"
)

// HTTP server hardening constants. These bound how long a client may hold a
// connection open doing nothing and how large its headers may be, without ever
// bounding the response body: a governed SSE stream can run for minutes, so a
// WriteTimeout or whole-request deadline is deliberately not set.
const (
	// serverReadHeaderTimeout bounds how long a client may take to send request
	// headers, defeating a slow-header (Slowloris) hold.
	serverReadHeaderTimeout = 10 * time.Second
	// serverIdleTimeout bounds how long an idle keep-alive connection lingers.
	serverIdleTimeout = time.Minute
	// serverMaxHeaderBytes caps the request header size at 64 KiB.
	serverMaxHeaderBytes = 64 * 1024
)

// Upstream transport hardening constants.
const (
	// dialTimeout bounds establishing a TCP connection to the upstream.
	dialTimeout = 10 * time.Second
	// dialKeepAlive is the keep-alive probe interval for upstream connections.
	dialKeepAlive = 30 * time.Second
	// tlsHandshakeTimeout bounds the upstream TLS handshake.
	tlsHandshakeTimeout = 10 * time.Second
	// maxIdleConnsPerHost bounds pooled idle connections to a single upstream.
	maxIdleConnsPerHost = 31
)

// HardenedServer builds an http.Server with leash's request-hardening timeouts
// applied. It sets ReadHeaderTimeout, IdleTimeout, and MaxHeaderBytes but leaves
// WriteTimeout and any whole-request deadline unset on purpose: a long streaming
// (SSE) response must be allowed to run for as long as the upstream keeps
// sending, and a write deadline would sever it mid-stream. Both the standalone
// gateway and the embedded wrapper server are built through here so the two can
// never drift.
func HardenedServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

// newUpstreamClient builds the HTTP client leash uses to reach the provider. The
// transport bounds connection setup (dial, TLS) and, when headerTimeout is
// non-zero, how long the upstream may take to send response headers, so a
// reasoning model that thinks for minutes before its first byte is tolerated
// while a dead upstream is not. There is deliberately no overall client timeout:
// once headers arrive, the streamed body may run for as long as it needs. A zero
// headerTimeout disables the response-header deadline entirely.
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
