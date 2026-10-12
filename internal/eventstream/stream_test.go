package eventstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestEitherSocketReconnectsIndependently(t *testing.T) {
	for _, restart := range []Kind{Mail, PIM} {
		t.Run(string(restart), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			var mail, pim atomic.Int32
			otherReady := make(chan struct{})
			closed := make(chan struct{}, 3)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				kind, count := Mail, &mail
				if strings.HasSuffix(r.URL.Path, "/pim/events") {
					kind, count = PIM, &pim
				}
				n := count.Add(1)
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { conn.CloseNow(); closed <- struct{}{} }()
				if kind != restart {
					if n == 1 {
						close(otherReady)
					}
				} else {
					select {
					case <-otherReady:
					case <-ctx.Done():
						return
					}
					if n == 1 {
						conn.Close(4401, "session_expired")
						return
					}
					conn.Write(ctx, websocket.MessageText, []byte(`{"type":"reconnected"}`))
				}
				conn.Read(ctx)
			}))
			defer srv.Close()
			matched := false
			err := Run(ctx, Config{APIURL: srv.URL, Token: "test", IdentityID: "MBX", Reconnect: true}, func(u Update) bool {
				if u.Kind == restart && strings.Contains(string(u.Frame), "reconnected") {
					matched = true
					return true
				}
				return false
			})
			wantMail, wantPIM := int32(1), int32(1)
			if restart == Mail {
				wantMail = 2
			} else {
				wantPIM = 2
			}
			if err != nil || !matched || mail.Load() != wantMail || pim.Load() != wantPIM {
				t.Fatalf("matched=%v mail=%d PIM=%d err=%v", matched, mail.Load(), pim.Load(), err)
			}
			for range 3 {
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("reader left open")
				}
			}
		})
	}
}

func TestOptionalPIMRefusalKeepsMail(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		reconnect bool
	}{
		{"forbidden", 403, `{"error":"forbidden"}`, true},
		{"not_found", 404, `{"error":"not_found"}`, true},
		{"unprovisioned_without_reconnect", 404, `{"error":"pim_unprovisioned"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			noticed := make(chan struct{})
			var pimDials atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/pim/events") {
					pimDials.Add(1)
					w.WriteHeader(tc.status)
					w.Write([]byte(tc.body))
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				select {
				case <-noticed:
				case <-ctx.Done():
					return
				}
				conn.Write(ctx, websocket.MessageText, []byte(`{"type":"message.new"}`))
				conn.Read(ctx)
			}))
			defer srv.Close()
			matched := false
			err := Run(ctx, Config{APIURL: srv.URL, IdentityID: "MBX", Reconnect: tc.reconnect}, func(u Update) bool {
				if u.Kind == PIM && u.Terminal {
					close(noticed)
				}
				if strings.Contains(string(u.Frame), "message.new") {
					matched = true
					return true
				}
				return false
			})
			if err != nil || !matched || pimDials.Load() != 1 {
				t.Fatalf("matched=%v PIM dials=%d err=%v", matched, pimDials.Load(), err)
			}
		})
	}
}

func TestPIMRevocationStopsBothReaders(t *testing.T) {
	for _, handshake := range []bool{true, false} {
		t.Run(map[bool]string{true: "HTTP401", false: "close4403"}[handshake], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			mailReady, mailClosed := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pim := strings.HasSuffix(r.URL.Path, "/pim/events")
				if pim {
					select {
					case <-mailReady:
					case <-ctx.Done():
						return
					}
					if handshake {
						w.WriteHeader(401)
						return
					}
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if pim {
					conn.Close(4403, "credential_revoked")
					return
				}
				defer close(mailClosed)
				close(mailReady)
				conn.Read(ctx)
			}))
			defer srv.Close()
			err := Run(ctx, Config{APIURL: srv.URL, IdentityID: "MBX", Reconnect: true}, func(Update) bool { return false })
			var failure *Failure
			if !errors.As(err, &failure) || !failure.Unauthorized() {
				t.Fatalf("expected revocation, got %v", err)
			}
			select {
			case <-mailClosed:
			case <-time.After(time.Second):
				t.Fatal("mail survived revocation")
			}
		})
	}
}

func TestMatchCancelsAnUnfinishedPIMUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pimStarted, pimCanceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pim/events") {
			close(pimStarted)
			<-r.Context().Done()
			close(pimCanceled)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		select {
		case <-pimStarted:
		case <-ctx.Done():
			return
		}
		conn.Write(ctx, websocket.MessageText, []byte(`{"type":"message.new"}`))
		conn.Read(ctx)
	}))
	defer srv.Close()
	err := Run(ctx, Config{APIURL: srv.URL, IdentityID: "MBX", Reconnect: true}, func(u Update) bool { return len(u.Frame) > 0 })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-pimCanceled:
	case <-time.After(time.Second):
		t.Fatal("upgrade survived match")
	}
}
