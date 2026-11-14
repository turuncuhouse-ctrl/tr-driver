package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"necipdrive/internal/version"
)

// Webhook events posted to BACKUP_WEBHOOK_URL (e.g. an ntfy topic URL).
const (
	EventSuccess = "backup.success"
	EventPartial = "backup.partial"
	EventFailed  = "backup.failed"
	EventStale   = "backup.stale"
)

// WebhookPayload is the JSON body posted to the webhook URL.
type WebhookPayload struct {
	Event   string    `json:"event"`
	Product string    `json:"product"`
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
	Message string    `json:"message,omitempty"`
	Run     *Run      `json:"run,omitempty"`
}

// Notify posts a backup event. A failing webhook never fails the backup run;
// the error is only logged by the caller.
func Notify(webhookURL, event string, run *Run, message string) error {
	if webhookURL == "" {
		return nil
	}
	body, err := json.Marshal(WebhookPayload{
		Event:   event,
		Product: version.ProductName,
		Version: version.Version,
		Time:    time.Now().UTC(),
		Message: message,
		Run:     run,
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
