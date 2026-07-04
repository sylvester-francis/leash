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
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

const (
	webhookQueue   = 256
	webhookTimeout = 5 * time.Second
)

// WebhookNotifier posts a JSON event to an operator-configured URL when a run
// approaches a budget (a warning) or a boundary stops it. Delivery is
// best-effort and off the request path: events queue on a buffered channel
// drained by one worker, and a full queue drops the event rather than block a
// governed call. Embeds NopObserver for the events it does not handle.
type WebhookNotifier struct {
	NopObserver
	url    string
	client *http.Client
	logger *slog.Logger
	now    func() time.Time
	ch     chan webhookEvent
}

type webhookEvent struct {
	Event     string  `json:"event"` // "warning" or "stopped"
	Run       string  `json:"run"`
	Reason    string  `json:"reason"`
	Used      float64 `json:"used,omitempty"`
	Limit     float64 `json:"limit,omitempty"`
	Fraction  float64 `json:"fraction,omitempty"`
	Calls     int64   `json:"calls"`
	TotalCost float64 `json:"total_cost"`
	At        string  `json:"at"`
}

// NewWebhookNotifier starts a notifier posting to url. now supplies the event
// timestamp. The worker runs for the process lifetime.
func NewWebhookNotifier(url string, logger *slog.Logger, now func() time.Time) *WebhookNotifier {
	n := &WebhookNotifier{
		url:    url,
		client: &http.Client{Timeout: webhookTimeout},
		logger: logger,
		now:    now,
		ch:     make(chan webhookEvent, webhookQueue),
	}
	go n.run()
	return n
}

// BudgetWarning enqueues a warning event.
func (n *WebhookNotifier) BudgetWarning(s *policy.State, st policy.BudgetStatus) {
	n.enqueue(webhookEvent{
		Event: "warning", Run: s.RunID, Reason: st.Reason,
		Used: st.Used, Limit: st.Limit, Fraction: st.Fraction,
		Calls: s.Calls, TotalCost: s.TotalCost, At: n.now().UTC().Format(time.RFC3339),
	})
}

// RunStopped enqueues a stopped event.
func (n *WebhookNotifier) RunStopped(s *policy.State) {
	n.enqueue(webhookEvent{
		Event: "stopped", Run: s.RunID, Reason: s.StopReason,
		Calls: s.Calls, TotalCost: s.TotalCost, At: n.now().UTC().Format(time.RFC3339),
	})
}

func (n *WebhookNotifier) enqueue(e webhookEvent) {
	select {
	case n.ch <- e:
	default:
		n.logger.Warn("webhook queue full; dropping event", "event", e.Event, "run", e.Run)
	}
}

func (n *WebhookNotifier) run() {
	for e := range n.ch {
		n.post(e)
	}
}

func (n *WebhookNotifier) post(e webhookEvent) {
	body, err := json.Marshal(e)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("webhook request build failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		n.logger.Warn("webhook post failed", "url", n.url, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.logger.Warn("webhook returned non-2xx", "url", n.url, "status", resp.StatusCode)
	}
}
