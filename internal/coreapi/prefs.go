package coreapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// PrefsScope selects which path spelling addresses the preference blob.
//
// Both reach the SAME document: prefs are keyed by IDENTITY (core serves one
// implementation over `mailboxId` and `identityId`), and an identity and its
// mail store share one ULID today. The identity spelling is the honest one for
// a mail-less identity — a calendar-only login still has a theme and a
// language — while the mailbox spelling is what a mail client already holds.
type PrefsScope string

const (
	PrefsMailbox  PrefsScope = "mailboxes"
	PrefsIdentity PrefsScope = "identities"
)

// Prefs is the opaque client preferences blob plus its version counter. Core
// validates nothing inside Prefs — it is the client's own JSON — but the
// version is what makes concurrent writes safe.
type Prefs struct {
	Prefs   map[string]any `json:"prefs"`
	Version int64          `json:"version"`
}

func prefsPath(scope PrefsScope, id string) string {
	return "/" + string(scope) + "/" + escapeSegment(id) + "/prefs"
}

// GetPrefs reads the preferences blob. Self-service: an app password may read
// its own.
func (c *Client) GetPrefs(ctx context.Context, scope PrefsScope, id string) (*Prefs, error) {
	var out Prefs
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: prefsPath(scope, id), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PutPrefs overwrites the blob and returns the new version. ifMatch is the
// compare-and-swap guard: pass the version a read returned and a stale write
// is refused 412 version_conflict rather than silently clobbering another
// device; "0" is a create-only write; "" (or "*") is an unconditional upsert.
// Bodies over 64 KiB are refused 413 too_large.
func (c *Client) PutPrefs(ctx context.Context, scope PrefsScope, id string, prefs map[string]any, ifMatch string) (*Prefs, error) {
	if prefs == nil {
		prefs = map[string]any{} // the field is required; nil would marshal to null
	}
	headers := map[string]string{}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	var out Prefs
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: prefsPath(scope, id),
		body:        mustJSON(map[string]any{"prefs": prefs}),
		contentType: "application/json", headers: headers, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DecodePrefs unmarshals the opaque blob into a caller-supplied shape, for a
// client that knows its own schema.
func (p *Prefs) DecodePrefs(out any) error {
	raw, err := json.Marshal(p.Prefs)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
