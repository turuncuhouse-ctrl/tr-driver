package backup

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotify(t *testing.T) {
	var got WebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	run := &Run{ID: "r1", Status: StatusSuccess}
	if err := Notify(server.URL, EventSuccess, run, ""); err != nil {
		t.Fatal(err)
	}
	if got.Event != EventSuccess || got.Product != "TR Driver" || got.Run == nil || got.Run.ID != "r1" {
		t.Fatalf("bad payload: %+v", got)
	}

	// Empty URL is a silent no-op; a dead URL returns an error but never panics.
	if err := Notify("", EventFailed, nil, "x"); err != nil {
		t.Fatalf("empty url: %v", err)
	}
	if err := Notify("http://127.0.0.1:1/nope", EventFailed, nil, "x"); err == nil {
		t.Fatal("expected error for dead url")
	}
}
