package localtel

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A WebSocket protocol-level ping (what OkHttp sends every 25s) is answered
// by the server library with a pong *write* on the same connection. That write
// used to fail once a per-message write deadline had expired, killing the
// connection after ~50s and causing constant reconnects.
func TestWSSurvivesProtocolPingAfterIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps 11s")
	}
	h, _ := testHandler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handleWS(w, r, "user-a", "dev-1")
	}))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: " + strings.TrimPrefix(srv.URL, "http://") +
		"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"))
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	mask := []byte{1, 2, 3, 4}
	// Masked text frame carrying an application-level ping, then idle past the
	// old 10s write deadline.
	payload := []byte(`{"type":"ping"}`)
	frame := []byte{0x81, byte(0x80 | len(payload))}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, _ = conn.Write(frame)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	hdr := make([]byte, 2)
	if _, err := br.Read(hdr); err != nil {
		t.Fatalf("no app-level pong: %v", err)
	}
	buf := make([]byte, hdr[1]&0x7f)
	_, _ = br.Read(buf)

	time.Sleep(11 * time.Second)

	// Masked empty protocol ping.
	_, _ = conn.Write(append([]byte{0x89, 0x80}, mask...))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, 2)
	if _, err := br.Read(got); err != nil {
		t.Fatalf("connection died instead of answering the protocol ping: %v", err)
	}
	if got[0] != 0x8A {
		t.Fatalf("expected pong frame, got %#x", got[0])
	}
}
