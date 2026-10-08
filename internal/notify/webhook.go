package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/storage"
)

type webhookType string

const (
	TypeDiscord webhookType = "discord"
	TypeSlack   webhookType = "slack"
	TypeGeneric webhookType = "generic"
)

// Webhook posts a JSON payload to a URL when changes are detected.
type Webhook struct {
	url         string
	wtype       webhookType
	httpClient  *http.Client
}

// NewWebhook creates a Webhook notifier.
func NewWebhook(url, wtype string) *Webhook {
	return &Webhook{
		url:        url,
		wtype:      webhookType(wtype),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Notify sends a webhook payload summarising the diff cycle.
func (w *Webhook) Notify(ctx context.Context, events []storage.Event) error {
	var payload any
	switch w.wtype {
	case TypeDiscord:
		payload = w.discordPayload(events)
	case TypeSlack:
		payload = w.slackPayload(events)
	default:
		payload = w.genericPayload(events)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

func summarise(events []storage.Event) (added, removed int) {
	for _, e := range events {
		if e.EventType == "added" {
			added++
		} else {
			removed++
		}
	}
	return
}

func (w *Webhook) discordPayload(events []storage.Event) any {
	added, removed := summarise(events)
	return map[string]any{
		"content": fmt.Sprintf("**jelly-diff**: %d added, %d removed", added, removed),
	}
}

func (w *Webhook) slackPayload(events []storage.Event) any {
	added, removed := summarise(events)
	return map[string]any{
		"text": fmt.Sprintf("*jelly-diff*: %d added, %d removed", added, removed),
	}
}

func (w *Webhook) genericPayload(events []storage.Event) any {
	added, removed := summarise(events)
	return map[string]any{
		"added":   added,
		"removed": removed,
		"total":   len(events),
		"time":    time.Now().UTC().Format(time.RFC3339),
	}
}
