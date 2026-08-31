package coreapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// Prefs is the opaque client preferences blob plus its version counter. Core
// validates nothing inside Prefs — it is the client's own JSON — but the
// version is what makes concurrent writes safe.
type Prefs struct {
	Prefs   map[string]any `json:"prefs"`
	Version int64          `json:"version"`
}

// prefsPath addresses the blob by identity: prefs are keyed by IDENTITY (a
// calendar-only login still has a theme and a language), and an identity and
// its mail store share one ULID, so the id a mail client holds works here too.
func prefsPath(id string) string {
	return "/identities/" + escapeSegment(id) + "/prefs"
}

// GetPrefs reads the preferences blob. Self-service: an app password may read
// its own.
func (c *Client) GetPrefs(ctx context.Context, id string) (*Prefs, error) {
	var out Prefs
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: prefsPath(id), idempotent: true,
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
func (c *Client) PutPrefs(ctx context.Context, id string, prefs map[string]any, ifMatch string) (*Prefs, error) {
	if prefs == nil {
		prefs = map[string]any{} // the field is required; nil would marshal to null
	}
	headers := map[string]string{}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	var out Prefs
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: prefsPath(id),
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
