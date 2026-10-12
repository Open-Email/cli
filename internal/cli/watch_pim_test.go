package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWatchPIMRecoversWithoutRedialingMail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var mailDials, pimDials atomic.Int32
	var optedOut atomic.Bool
	mailReady := make(chan struct{})
	closed := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("socket missing bearer")
		}
		kind := "mail"
		switch r.URL.Path {
		case "/api/v1/mailboxes/" + testMbx + "/events":
			if mailDials.Add(1) == 1 {
				close(mailReady)
			}
			optedOut.Store(r.URL.Query().Get("pim") == "off")
		case "/api/v1/identities/" + testMbx + "/pim/events":
			kind = "pim"
			if pimDials.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":"pim_unprovisioned"}`))
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { conn.CloseNow(); closed <- kind }()
		writeFrames(ctx, conn, `{"type":"ready","data":{}}`)
		if kind == "pim" {
			select {
			case <-mailReady:
			case <-ctx.Done():
				return
			}
			writeFrames(ctx, conn, `{"type":"pim.changed","data":{"CalendarEvent":"2"}}`)
		}
		conn.Read(ctx) // observe client cancellation/close, including on --until
	}))
	defer srv.Close()
	a, frames, _ := newWatchTestApp(srv.URL)
	err := runWatch(ctx, a, "--until", "pim.*")
	if err != nil {
		t.Fatalf("PIM event should satisfy --until after provisioning: %v", err)
	}
	if !optedOut.Load() || mailDials.Load() != 1 || pimDials.Load() != 2 {
		t.Fatalf("opt-out=%v mail dials=%d PIM dials=%d", optedOut.Load(), mailDials.Load(), pimDials.Load())
	}
	lines := strings.Split(strings.TrimSpace(frames.String()), "\n")
	if !strings.Contains(lines[len(lines)-1], `"pim.changed"`) {
		t.Fatalf("matching PIM frame must be last: %s", frames.String())
	}
	for range 2 {
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("watch left a socket open after matching")
		}
	}
}
