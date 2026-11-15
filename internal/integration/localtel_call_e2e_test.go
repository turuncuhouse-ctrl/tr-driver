package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"necipdrive/internal/auth"
	"necipdrive/internal/localtel"
)

// End-to-end: two members, real DB accounts, real handler, fake WS clients.
// Covers invite/ringing/accept/audio/hangup, multi-session ringing, and the
// distinct unknown / offline / busy / self outcomes sent back to the caller.
func TestLocalTelCallFlowEndToEnd(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	authSvc, emailA := registerUser(t, db, cfg)
	_, emailB := registerUser(t, db, cfg)
	_, emailC := registerUser(t, db, cfg)
	ids := map[string]string{}
	for k, e := range map[string]string{"a": emailA, "b": emailB, "c": emailC} {
		var id string
		if err := db.QueryRow(ctx, `select id::text from users where email = $1`, e).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[k] = id
	}
	vault, _ := localtel.NewVault(cfg.SessionSecret)
	svc, err := localtel.NewService(db, vault)
	if err != nil {
		t.Fatal(err)
	}
	accts := map[string]*localtel.Account{}
	for k, id := range ids {
		a, err := svc.EnsureAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		accts[k] = a
	}
	tokens := map[string]string{}
	for k, id := range ids {
		_, tok, err := authSvc.CreateDevice(ctx, id, "dev-"+k)
		if err != nil {
			t.Fatal(err)
		}
		tokens[k] = tok
	}
	hub := localtel.NewHub()
	srv := httptest.NewServer(auth.NewHandler(authSvc, nil).RequireAuth(localtel.NewHandler(svc, hub)))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + localtel.BasePath + "ws"

	dial := func(user string) *websocket.Conn {
		c, err := websocket.NewConfig(wsURL, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		c.Header = http.Header{"Authorization": []string{"Bearer " + tokens[user]}}
		conn, err := websocket.DialConfig(c)
		if err != nil {
			t.Fatalf("dial %s: %v", user, err)
		}
		return conn
	}
	send := func(c *websocket.Conn, v map[string]any) {
		raw, _ := json.Marshal(v)
		if err := websocket.Message.Send(c, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	recv := func(c *websocket.Conn, d time.Duration) (map[string]any, bool) {
		_ = c.SetReadDeadline(time.Now().Add(d))
		var s string
		if err := websocket.Message.Receive(c, &s); err != nil {
			return nil, false
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		return m, true
	}
	expect := func(c *websocket.Conn, typ, payload string) map[string]any {
		t.Helper()
		m, ok := recv(c, 2*time.Second)
		if !ok {
			t.Fatalf("expected %q, got nothing", typ)
		}
		if m["type"] != typ || (payload != "" && m["payload"] != payload) {
			t.Fatalf("expected %q/%q, got %v", typ, payload, m)
		}
		return m
	}
	waitConnected := func(user string, n int) {
		for i := 0; i < 50 && hub.ConnectedCount(ids[user]) < n; i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}

	// 1. Unknown target -> "unknown_target" (never "busy").
	a := dial("a")
	defer a.Close()
	waitConnected("a", 1)
	send(a, map[string]any{"type": "invite", "callId": "c-unknown", "number": "05999999999"})
	expect(a, "reject", "unknown_target")

	// 2. Known target without any connection -> "callee_offline".
	send(a, map[string]any{"type": "invite", "callId": "c-offline", "number": accts["b"].Extension})
	expect(a, "reject", "callee_offline")

	// 3. Self call.
	send(a, map[string]any{"type": "invite", "callId": "c-self", "number": accts["a"].Extension})
	expect(a, "reject", "self")

	// 4. Real call to B, who has two sessions: both ring; resolves by
	// extension, username, and registered phone (several spellings).
	if _, err := svc.SetPhone(ctx, ids["b"], "0 (532) 111 22 33"); err != nil {
		t.Fatal(err)
	}
	b1, b2 := dial("b"), dial("b")
	defer b1.Close()
	defer b2.Close()
	waitConnected("b", 2)
	for i, dialed := range []string{accts["b"].Extension, accts["b"].Username, "+90 532 111 22 33", "5321112233", "05321112233"} {
		id := "c-ring-" + string(rune('a'+i))
		send(a, map[string]any{"type": "invite", "callId": id, "number": dialed})
		m1 := expect(b1, "invite", "")
		m2 := expect(b2, "invite", "")
		if m1["callId"] != id || m2["callId"] != id {
			t.Fatalf("dial %q: wrong call ids %v %v", dialed, m1, m2)
		}
		if m1["number"] != accts["a"].Extension && !strings.HasPrefix(m1["number"].(string), "+") {
			t.Fatalf("caller identity not an address: %v", m1["number"])
		}
		send(a, map[string]any{"type": "cancel", "callId": id})
		expect(b1, "cancel", "")
		expect(b2, "cancel", "")
	}

	// 5. Full flow: ringing -> accept -> audio both ways -> hangup.
	send(a, map[string]any{"type": "invite", "callId": "c-main", "number": accts["b"].Extension})
	expect(b1, "invite", "")
	expect(b2, "invite", "")
	send(b1, map[string]any{"type": "ringing", "callId": "c-main"})
	expect(a, "ringing", "")
	send(b1, map[string]any{"type": "accept", "callId": "c-main"})
	expect(a, "accept", "")
	send(a, map[string]any{"type": "audio", "callId": "c-main", "payload": "QUJD"})
	if m := expect(b1, "audio", "QUJD"); m["callId"] != "c-main" {
		t.Fatal("audio call id")
	}
	send(b1, map[string]any{"type": "audio", "callId": "c-main", "payload": "REVG"})
	expect(a, "audio", "REVG")

	// 6. C calls B while B is in the answered call -> "busy" (real busy).
	c := dial("c")
	defer c.Close()
	waitConnected("c", 1)
	send(c, map[string]any{"type": "invite", "callId": "c-busy", "number": accts["b"].Extension})
	expect(c, "reject", "busy")

	// 7. Hangup frees B; a new call from C now rings.
	send(a, map[string]any{"type": "hangup", "callId": "c-main"})
	expect(b1, "hangup", "")
	send(c, map[string]any{"type": "invite", "callId": "c-after", "number": accts["b"].Extension})
	expect(b1, "invite", "")

	// 8. Stale state: when B's every session vanishes mid-call, C is told the
	// call is over and B is not stuck "busy" afterwards.
	send(b1, map[string]any{"type": "accept", "callId": "c-after"})
	expect(c, "accept", "")
	b1.Close()
	b2.Close()
	m := expect(c, "hangup", "peer_disconnected")
	if m["callId"] != "c-after" {
		t.Fatal("wrong call id on peer_disconnected")
	}
	b3 := dial("b")
	defer b3.Close()
	waitConnected("b", 1)
	send(c, map[string]any{"type": "invite", "callId": "c-fresh", "number": accts["b"].Extension})
	expect(b3, "invite", "")

	// Directory lists the other members by name+extension with online state.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+localtel.BasePath+"directory", nil)
	req.Header.Set("Authorization", "Bearer "+tokens["a"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var dir []localtel.DirectoryEntry
	if err := json.NewDecoder(resp.Body).Decode(&dir); err != nil || resp.StatusCode != 200 {
		t.Fatalf("directory: %d %v", resp.StatusCode, err)
	}
	foundB := false
	for _, e := range dir {
		if e.Extension == accts["a"].Extension {
			t.Fatal("directory must not list the caller")
		}
		if e.Extension == accts["b"].Extension {
			foundB = e.Online
		}
	}
	if !foundB {
		t.Fatal("directory should list B as online")
	}
}
