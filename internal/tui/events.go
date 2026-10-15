package tui

import (
	"context"
	"encoding/json"

	"github.com/Open-Email/cli/internal/eventstream"
	tea "github.com/charmbracelet/bubbletea"
)

// liveEvent is one mail or PIM change signal for the selected identity.
type liveEvent struct {
	paneID int64
	typ    string
}

func (m liveEvent) targetPaneID() int64 { return m.paneID }

// liveState reports the subscription's connection state for the header
// indicator. terminal means the goroutine is done (deterministic refusal or
// mailbox gone) and no further messages will arrive.
type liveState struct {
	paneID    int64
	connected bool
	detail    string
	terminal  bool
}

func (m liveState) targetPaneID() int64 { return m.paneID }

// subscription combines mail and PIM change signals. Each upstream reconnects
// independently; canceling ctx closes both readers before closing ch.
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
	connected := map[eventstream.Kind]bool{}
	state := map[eventstream.Kind]string{eventstream.Mail: "connecting", eventstream.PIM: "connecting"}
	err := eventstream.Run(ctx, eventstream.Config{APIURL: apiURL, Token: token, IdentityID: mailboxID, Reconnect: true}, func(update eventstream.Update) bool {
		if update.Frame != nil {
			var frame struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(update.Frame, &frame) == nil && frame.Type != "" && frame.Type != "ready" {
				s.push(liveEvent{paneID: paneID, typ: frame.Type})
			}
			return false
		}
		connected[update.Kind] = update.Connected
		switch {
		case update.Connected:
			state[update.Kind] = "live"
		case update.Terminal:
			state[update.Kind] = "unavailable"
		default:
			state[update.Kind] = "reconnecting"
		}
		detail := state[eventstream.Mail]
		if !connected[eventstream.PIM] {
			detail += "; PIM " + state[eventstream.PIM]
		}
		s.push(liveState{paneID: paneID, connected: connected[eventstream.Mail], detail: detail})
		return false
	})
	if ctx.Err() == nil {
		detail := "off"
		if err != nil {
			detail = err.Error()
		}
		s.push(liveState{paneID: paneID, detail: detail, terminal: true})
	}
}
