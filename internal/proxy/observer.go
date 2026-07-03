package proxy

import (
	"github.com/sylvester-francis/leash/internal/meter"
	"github.com/sylvester-francis/leash/internal/policy"
)

// Observer is the side channel for governance events. It subsumes the older
// OnStop callback: the CLI's stop-line printer and the metrics registry are both
// Observers. Every method must be safe for concurrent use; the zero cost of the
// default (NopObserver) means the proxy can always call it unconditionally.
//
// Deliberately, no method carries a run id. Run ids are unbounded-cardinality
// identifiers; per-run accounting already lives in the ledger (that is what
// `leash ps` reads). Keeping ids out of this seam keeps metrics cardinality
// bounded by construction.
type Observer interface {
	// CallForwarded is reported once per call sent upstream, with the metered
	// usage and whether the token meter was blind (no usage on the wire).
	CallForwarded(provider meter.Provider, usage policy.Usage, blind bool)
	// CallRefused is reported once per call refused by a boundary, whether the
	// boundary tripped on this call or the run was already stopped.
	CallRefused(provider meter.Provider, reason string)
	// RunStopped is reported once, at the moment a run transitions to stopped.
	RunStopped(state *policy.State)
	// UpstreamError is reported when a forwarded call fails to reach the upstream
	// or read its response.
	UpstreamError()
}

// NopObserver is the default Observer: it ignores every event. It is the value
// New installs when Config.Observer is nil.
type NopObserver struct{}

// CallForwarded ignores the event.
func (NopObserver) CallForwarded(meter.Provider, policy.Usage, bool) {}

// CallRefused ignores the event.
func (NopObserver) CallRefused(meter.Provider, string) {}

// RunStopped ignores the event.
func (NopObserver) RunStopped(*policy.State) {}

// UpstreamError ignores the event.
func (NopObserver) UpstreamError() {}

// MultiObserver fans one event out to several Observers in order. It lets serve
// run the metrics registry and the stop-line printer from the single seam.
type MultiObserver []Observer

// CallForwarded forwards to each observer.
func (m MultiObserver) CallForwarded(p meter.Provider, u policy.Usage, blind bool) {
	for _, o := range m {
		o.CallForwarded(p, u, blind)
	}
}

// CallRefused forwards to each observer.
func (m MultiObserver) CallRefused(p meter.Provider, reason string) {
	for _, o := range m {
		o.CallRefused(p, reason)
	}
}

// RunStopped forwards to each observer.
func (m MultiObserver) RunStopped(s *policy.State) {
	for _, o := range m {
		o.RunStopped(s)
	}
}

// UpstreamError forwards to each observer.
func (m MultiObserver) UpstreamError() {
	for _, o := range m {
		o.UpstreamError()
	}
}

// stopLineObserver adapts a stop-line callback to the Observer seam, so the CLI
// can keep printing exactly one line when a run stops.
type stopLineObserver struct {
	onStop func(*policy.State)
}

// CallForwarded ignores the event.
func (stopLineObserver) CallForwarded(meter.Provider, policy.Usage, bool) {}

// CallRefused ignores the event.
func (stopLineObserver) CallRefused(meter.Provider, string) {}

// RunStopped invokes the callback.
func (o stopLineObserver) RunStopped(s *policy.State) { o.onStop(s) }

// UpstreamError ignores the event.
func (stopLineObserver) UpstreamError() {}

// StopLineObserver returns an Observer that calls onStop once when a run stops.
// It is the replacement for the former Config.OnStop callback.
func StopLineObserver(onStop func(*policy.State)) Observer {
	return stopLineObserver{onStop: onStop}
}
