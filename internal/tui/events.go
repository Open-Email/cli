package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
)

// liveEvent is one change signal from the mailbox's events channel.
type liveEvent struct {
	paneID int64
	typ    string
}

// liveState reports the subscription's connection state for the header
// indicator. terminal means the goroutine is done (deterministic refusal or
// mailbox gone) and no further messages will arrive.
type liveState struct {
	paneID    int64
	connected bool
	detail    string
	terminal  bool
}

// subscription tails GET /mailboxes/:id/events over WebSocket and pumps
// liveEvent/liveState messages. The dial/reconnect discipline mirrors
// `openemail watch` (src: internal/cli/watch.go): ping keepalive, reconnect
// with backoff on transient drops and 4401 session expiry, stop for good on
// deterministic 4xx. Canceling ctx ends the goroutine and closes ch.
type subscription struct {
	ch chan tea.Msg
}

func subscribe(ctx context.Context, apiURL, token, mailboxID string, paneID int64) *subscription {
	s := &subscription{ch: make(chan tea.Msg, 64)}
	go s.run(ctx, apiURL, token, mailboxID, paneID)
	return s
}

// wait is the bubbletea re-arm command: deliver the next message, or nil once
// the subscription is closed (a nil Msg is discarded by the runtime).
func (s *subscription) wait() tea.Cmd {
	return func() tea.Msg {
		m, ok := <-s.ch
		if !ok {
			return nil
		}
		return m
	}
}

// push never blocks the socket reader: if the UI is behind, the oldest queued
// message is dropped — events are refresh signals, so losing an intermediate
// one is harmless as long as a later one lands.
func (s *subscription) push(m tea.Msg) {
	select {
	case s.ch <- m:
	default:
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- m:
		default:
		}
	}
}

func (s *subscription) run(ctx context.Context, apiURL, token, mailboxID string, paneID int64) {
	defer close(s.ch)
	const backoffFloor = 500 * time.Millisecond
	backoff := backoffFloor
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		retryable, err := s.session(ctx, apiURL, token, mailboxID, paneID)
		if ctx.Err() != nil {
			return
		}
		if !retryable {
			detail := "off"
			if err != nil {
				detail = err.Error()
			}
			s.push(liveState{paneID: paneID, detail: detail, terminal: true})
			return
		}
		if time.Since(start) >= 30*time.Second {
			backoff = backoffFloor // a session that stayed up is not a failure
		}
		s.push(liveState{paneID: paneID, detail: "reconnecting"})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

// session runs one WebSocket connection to completion. Returns whether a
// reconnect is warranted.
func (s *subscription) session(ctx context.Context, apiURL, token, mailboxID string, paneID int64) (bool, error) {
	wsURL, err := eventsURL(apiURL, mailboxID)
	if err != nil {
		return false, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, resp, derr := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if derr != nil {
		// Pre-accept refusals are plain HTTP statuses; deterministic 4xx must
		// not reconnect (only 429 and transport/5xx are transient).
		if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return false, errors.New(httpRefusal(resp.StatusCode))
		}
		return true, derr
	}
	defer conn.CloseNow()

	s.push(liveState{paneID: paneID, connected: true, detail: "live"})

	// Keepalive: the channel is one-way; the client pings, the runtime answers
	// pong without waking the DO.
	pingCtx, pingStop := context.WithCancel(ctx)
	defer pingStop()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				if werr := conn.Write(pingCtx, websocket.MessageText, []byte("ping")); werr != nil {
					return
				}
			}
		}
	}()

	conn.SetReadLimit(1 << 20)
	for {
		typ, data, rerr := conn.Read(ctx)
		if rerr != nil {
			switch websocket.CloseStatus(rerr) {
			case 4401:
				return true, nil // session expiry: re-dial promptly
			case 4403:
				return false, errors.New("credential revoked") // do not reconnect
			case 4410:
				return false, errors.New("mailbox gone")
			}
			return true, rerr
		}
		if typ != websocket.MessageText {
			continue
		}
		line := strings.TrimSpace(string(data))
		if line == "" || line == "pong" {
			continue
		}
		var f struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &f) != nil || f.Type == "" || f.Type == "ready" {
			continue // handshake or unparseable — never a change signal
		}
		s.push(liveEvent{paneID: paneID, typ: f.Type})
	}
}

func httpRefusal(status int) string {
	switch status {
	case 401:
		return "not authorized"
	case 404:
		return "mailbox not found"
	case 410:
		return "mailbox being deleted"
	case 426:
		return "upgrade refused"
	}
	return "refused (HTTP " + strconv.Itoa(status) + ")"
}

// eventsURL builds the ws(s):// upgrade URL for a mailbox's events channel,
// mirroring internal/cli/watch.go's eventsURL.
func eventsURL(apiURL, mailboxID string) (string, error) {
	u, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/mailboxes/" + mailboxID + "/events"
	return u.String(), nil
}
