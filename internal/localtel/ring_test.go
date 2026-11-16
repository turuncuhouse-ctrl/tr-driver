package localtel

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recvSignal waits up to timeout for the next signal on ch.
func recvSignal(t *testing.T, ch <-chan []byte, timeout time.Duration, what string) (SignalMessage, bool) {
	t.Helper()
	select {
	case raw := <-ch:
		var msg SignalMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("%s: unmarshal: %v", what, err)
		}
		return msg, true
	case <-time.After(timeout):
		return SignalMessage{}, false
	}
}

// recvType skips unrelated signals until the wanted type arrives.
func recvType(t *testing.T, ch <-chan []byte, want string, timeout time.Duration, what string) SignalMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("%s: signal type %q not received in %s", what, want, timeout)
		}
		msg, ok := recvSignal(t, ch, remaining, what)
		if !ok {
			t.Fatalf("%s: signal type %q not received in %s", what, want, timeout)
		}
		if msg.Type == want {
			return msg
		}
	}
}

func assertNoSignal(t *testing.T, ch <-chan []byte, window time.Duration, what string) {
	t.Helper()
	select {
	case raw := <-ch:
		var msg SignalMessage
		_ = json.Unmarshal(raw, &msg)
		t.Fatalf("%s: unexpected signal %+v", what, msg)
	case <-time.After(window):
	}
}

// ringTestEnv wires a resolvable two-user world without a database so the
// full invite → retry → timeout path runs through routeSignal.
func ringTestEnv(t *testing.T) (*Handler, *Hub, *Client, *Client) {
	t.Helper()
	h, hub := testHandler(t)
	hub.ringRetryInterval = 100 * time.Millisecond
	hub.ringTimeout = 600 * time.Millisecond
	h.service.resolveForTest = func(_ context.Context, dial string) (string, bool) {
		return "user-b", strings.Contains(dial, "555")
	}
	h.service.identityForTest = func(_ context.Context, userID string) (string, string, bool) {
		return "+905550001122", "Ayşe Yılmaz", true
	}
	caller := hub.Register("user-a", "dev-1")
	callee := hub.Register("user-b", "dev-b")
	t.Cleanup(func() {
		hub.Unregister(caller)
		hub.Unregister(callee)
	})
	return h, hub, caller, callee
}

func TestInviteRingsUntilNoAnswer(t *testing.T) {
	h, hub, caller, callee := ringTestEnv(t)

	msg := SignalMessage{Type: "invite", CallID: "call-noanswer", Number: "05551112233"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if delivered := h.routeSignal(req, "user-a", caller.id, &msg); delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}

	// The callee's phone rings: first the invite, then at least one server
	// retry while nobody answers (GSM-like re-ring).
	first, ok := recvSignal(t, callee.SendChannel(), 2*time.Second, "initial invite")
	if !ok || first.Type != "invite" || first.CallID != "call-noanswer" {
		t.Fatalf("initial invite = %+v ok=%v", first, ok)
	}
	if first.Number != "+905550001122" || first.DisplayName != "Ayşe Yılmaz" {
		t.Fatalf("callee must see the caller identity, got %+v", first)
	}
	retry := recvType(t, callee.SendChannel(), "invite", 2*time.Second, "ring retry")
	if retry.CallID != "call-noanswer" {
		t.Fatalf("retry callId = %q", retry.CallID)
	}

	// After the deadline the caller learns "no answer" and the callee gets a
	// missed-call signal with the caller identity.
	reject := recvType(t, caller.SendChannel(), "reject", 3*time.Second, "timeout reject")
	if reject.Payload != "no_answer" || reject.CallID != "call-noanswer" {
		t.Fatalf("timeout reject = %+v, want no_answer", reject)
	}
	missed := recvType(t, callee.SendChannel(), "missed", 3*time.Second, "missed signal")
	if missed.Number != "+905550001122" || missed.DisplayName != "Ayşe Yılmaz" || missed.CallID != "call-noanswer" {
		t.Fatalf("missed = %+v", missed)
	}
	if hub.IsBusy("user-a") || hub.IsBusy("user-b") {
		t.Fatal("call still busy after the no-answer timeout")
	}
	// The missed call is also queued for a device that connects later.
	hub.mu.RLock()
	queued := len(hub.missed["user-b"])
	hub.mu.RUnlock()
	if queued == 0 {
		t.Fatal("missed call was not queued for later delivery")
	}
}

func TestAcceptStopsRingRetries(t *testing.T) {
	h, hub, caller, callee := ringTestEnv(t)

	msg := SignalMessage{Type: "invite", CallID: "call-accept", Number: "05551112233"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if delivered := h.routeSignal(req, "user-a", caller.id, &msg); delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	recvType(t, callee.SendChannel(), "invite", 2*time.Second, "initial invite")

	accept := SignalMessage{Type: "accept", CallID: "call-accept"}
	if delivered := h.routeSignal(req, "user-b", callee.id, &accept); delivered != 1 {
		t.Fatalf("accept delivered = %d, want 1", delivered)
	}
	got := recvType(t, caller.SendChannel(), "accept", 2*time.Second, "accept to caller")
	if got.CallID != "call-accept" {
		t.Fatalf("accept = %+v", got)
	}

	// Well past several retry intervals and the deadline: no re-invites, no
	// spurious no_answer.
	assertNoSignal(t, callee.SendChannel(), 900*time.Millisecond, "retries after accept")
	assertNoSignal(t, caller.SendChannel(), 200*time.Millisecond, "timeout after accept")
	if !hub.IsBusy("user-b") {
		t.Fatal("answered call must stay busy")
	}
}

func TestMissedDeliveredWhenCalleeReconnects(t *testing.T) {
	h, hub, caller, callee := ringTestEnv(t)

	msg := SignalMessage{Type: "invite", CallID: "call-offline", Number: "05551112233"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if delivered := h.routeSignal(req, "user-a", caller.id, &msg); delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	recvType(t, callee.SendChannel(), "invite", 2*time.Second, "initial invite")

	// The callee's app loses its connection before answering: the call ends
	// for the caller (peer_disconnected) and becomes a missed call.
	hub.Unregister(callee)
	got := recvType(t, caller.SendChannel(), "hangup", 3*time.Second, "call end for caller")
	if got.CallID != "call-offline" {
		t.Fatalf("hangup = %+v", got)
	}

	// Reconnecting later still shows the missed call.
	late := hub.Register("user-b", "dev-late")
	defer hub.Unregister(late)
	missed := recvType(t, late.SendChannel(), "missed", 2*time.Second, "missed after reconnect")
	if missed.CallID != "call-offline" || missed.Number != "+905550001122" {
		t.Fatalf("missed after reconnect = %+v", missed)
	}
	// The queue is drained after the flush.
	hub.mu.RLock()
	queued := len(hub.missed["user-b"])
	hub.mu.RUnlock()
	if queued != 0 {
		t.Fatalf("missed queue not drained: %d", queued)
	}
}

func TestCallerCancelStopsRinging(t *testing.T) {
	h, _, caller, callee := ringTestEnv(t)

	msg := SignalMessage{Type: "invite", CallID: "call-cancel", Number: "05551112233"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.routeSignal(req, "user-a", caller.id, &msg)
	recvType(t, callee.SendChannel(), "invite", 2*time.Second, "initial invite")

	cancel := SignalMessage{Type: "cancel", CallID: "call-cancel"}
	h.routeSignal(req, "user-a", caller.id, &cancel)
	recvType(t, callee.SendChannel(), "cancel", 2*time.Second, "cancel to callee")

	// No retries and no missed signal after an explicit cancel.
	assertNoSignal(t, callee.SendChannel(), 500*time.Millisecond, "retry after cancel")
	assertNoSignal(t, callee.SendChannel(), 300*time.Millisecond, "missed after cancel")
	assertNoSignal(t, caller.SendChannel(), 700*time.Millisecond, "timeout after cancel")
}

func TestFCMSenderSignsAndSendsHighPriority(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	accountJSON, err := json.Marshal(map[string]string{
		"project_id":   "demo-project",
		"client_email": "push@demo-project.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotBody string
	var gotPath string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.FormValue("assertion") == "" {
			t.Errorf("token request missing assertion")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	msgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer msgServer.Close()

	sender := newFCMSender(string(accountJSON))
	if sender == nil {
		t.Fatal("valid service account rejected")
	}
	sender.tokenURL = tokenServer.URL
	sender.messageURL = msgServer.URL

	if err := sender.send(t.Context(), "device-token-1", map[string]string{
		"type": "localtel_invite", "callId": "call-1", "number": "+905550001122", "displayName": "Ayşe",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.Contains(gotPath, "/projects/demo-project/messages:send") {
		t.Fatalf("path = %q", gotPath)
	}
	var payload struct {
		Message struct {
			Token   string            `json:"token"`
			Data    map[string]string `json:"data"`
			Android struct {
				Priority string `json:"priority"`
				TTL      string `json:"ttl"`
			} `json:"android"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body: %v (%s)", err, gotBody)
	}
	if payload.Message.Android.Priority != "HIGH" || payload.Message.Token != "device-token-1" {
		t.Fatalf("payload = %+v", payload.Message)
	}
	if payload.Message.Data["callId"] != "call-1" {
		t.Fatalf("data = %+v", payload.Message.Data)
	}

	// A JWT from signJWT verifies with the matching public key.
	mustSignSample(t, key)
	if newFCMSender("") != nil || newFCMSender("not json") != nil {
		t.Fatal("invalid configuration must disable the feature, not crash")
	}
}

// mustSignSample exercises signJWT and checks the signature round-trips.
func mustSignSample(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	token, err := signJWT(map[string]any{"iss": "x", "iat": 1, "exp": 2}, key)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature: %v", err)
	}
	return token
}
