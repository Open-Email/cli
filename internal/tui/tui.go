// Package tui implements the interactive console (`openemail ui`): a
// sidebar-and-content browser over the same coreapi client the flag commands
// use. v1 is read-only — every screen is a listing, a detail view, or a live
// tail; nothing here mutates the platform.
package tui

import (
	"context"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
)

// Options wires the console to an authenticated session. Role gates the
// sidebar (Accounts is system-only); APIURL and Token are needed alongside
// Client because the live-events WebSocket dials outside the REST client.
type Options struct {
	Client *coreapi.Client
	Role   string // coreapi.PrincipalAccount | coreapi.PrincipalSystem
	// AccountID is the account the key acts as — empty for system keys. It
	// scopes the account-tier screens (Do-Not-Send).
	AccountID string
	APIURL    string
	Token     string
	Profile   string
	Version   string
}

// Run starts the console and blocks until the user quits. Canceling ctx also
// ends the program and every event subscription it spawned.
func Run(ctx context.Context, opts Options) error {
	m := newRoot(ctx, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	m.closeAll()
	return err
}
