package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSubscriptionPIMStreamAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var mailDials, pimDials atomic.Int32
	var optedOut atomic.Bool
	closed := make(chan struct{}, 2)
	mailReady := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("socket missing bearer")
		}
		pim := r.URL.Path == "/api/v1/identities/MBX/pim/events"
		if pim {
			pimDials.Add(1)
		} else if r.URL.Path == "/api/v1/mailboxes/MBX/events" {
			mailDials.Add(1)
			optedOut.Store(r.URL.Query().Get("pim") == "off")
		} else {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { conn.CloseNow(); closed <- struct{}{} }()
		if !pim {
			close(mailReady)
		}
		conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ready"}`))
		if pim {
			select {
			case <-mailReady:
			case <-ctx.Done():
				return
			}
			conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pim.changed","data":{"ContactCard":"2"}}`))
		}
		conn.Read(ctx)
	}))
	defer srv.Close()
	s := subscribe(ctx, srv.URL, "test-token", "MBX", 42)
	seen := false
	for msg := range s.ch {
		if e, ok := msg.(liveEvent); ok && e.typ == "pim.changed" {
			if e.paneID != 42 {
				t.Errorf("event routed to pane %d", e.paneID)
			}
			seen = true
			cancel()
		}
	}
	if !seen || !optedOut.Load() || mailDials.Load() != 1 || pimDials.Load() != 1 {
		t.Fatalf("PIM seen=%v opt-out=%v mail dials=%d PIM dials=%d", seen, optedOut.Load(), mailDials.Load(), pimDials.Load())
	}
	for range 2 {
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("subscription left a socket open after cancellation")
		}
	}
}
