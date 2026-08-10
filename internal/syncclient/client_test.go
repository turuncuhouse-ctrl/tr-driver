package syncclient

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSnapshotAndChanges(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sync/snapshot" {
			io.WriteString(w, `{"cursor":7,"entries":[]}`)
			return
		}
		io.WriteString(w, `{"nextCursor":8,"changes":[]}`)
	}))
	defer s.Close()
	c := &Client{BaseURL: s.URL, Token: "token"}
	if v, e := c.Snapshot(context.Background()); e != nil || v.Cursor != 7 {
		t.Fatalf("%+v %v", v, e)
	}
	if v, e := c.Changes(context.Background(), 7, 10); e != nil || v.Cursor != 8 {
		t.Fatalf("%+v %v", v, e)
	}
}
func TestPutChunkConflictReturnsServerOffset(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upload-Offset") != "0" {
			t.Error("offset header")
		}
		w.Header().Set("Upload-Offset", "42")
		w.WriteHeader(http.StatusConflict)
	}))
	defer s.Close()
	n, e := (&Client{BaseURL: s.URL}).PutChunk(context.Background(), "session", 0, bytes.NewReader([]byte("x")), 1)
	if e == nil || n != 42 {
		t.Fatalf("offset=%d err=%v", n, e)
	}
}
