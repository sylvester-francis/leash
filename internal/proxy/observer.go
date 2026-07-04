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
	"github.com/sylvester-francis/leash/internal/meter"
	"github.com/sylvester-francis/leash/internal/policy"
)

// Observer is the side channel for governance events; it subsumes the old
// OnStop callback. Methods must be safe for concurrent use. No method carries a
// run id on purpose: run ids are unbounded cardinality, and per-run accounting
// lives in the ledger.
type Observer interface {
	// CallForwarded reports a call sent upstream, with its usage and blindness.
	CallForwarded(provider meter.Provider, usage policy.Usage, blind bool)
	// CallRefused reports a call a boundary refused (freshly or already stopped).
	CallRefused(provider meter.Provider, reason string)
	// RunStopped reports a run's transition to stopped.
	RunStopped(state *policy.State)
	// UpstreamError reports a failed upstream request or response read.
	UpstreamError()
	// LedgerError reports a failed durable write (a call or stop record).
	LedgerError()
}

// NopObserver ignores every event. New installs it when Config.Observer is nil.
type NopObserver struct{}

// CallForwarded ignores the event.
func (NopObserver) CallForwarded(meter.Provider, policy.Usage, bool) {}

// CallRefused ignores the event.
func (NopObserver) CallRefused(meter.Provider, string) {}

// RunStopped ignores the event.
func (NopObserver) RunStopped(*policy.State) {}

// UpstreamError ignores the event.
func (NopObserver) UpstreamError() {}

// LedgerError ignores the event.
func (NopObserver) LedgerError() {}

// MultiObserver fans each event out to several Observers in order.
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

// LedgerError forwards to each observer.
func (m MultiObserver) LedgerError() {
	for _, o := range m {
		o.LedgerError()
	}
}

// stopLineObserver adapts a stop-line callback to the Observer seam.
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

// LedgerError ignores the event.
func (stopLineObserver) LedgerError() {}

// StopLineObserver returns an Observer that calls onStop once when a run stops.
func StopLineObserver(onStop func(*policy.State)) Observer {
	return stopLineObserver{onStop: onStop}
}
