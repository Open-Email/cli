package coreapi

import (
	"context"
	"net/http"
)

// SieveScript is one row of the script listing (no body).
type SieveScript struct {
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	UpdatedAt int64  `json:"updatedAt"`
}

// SieveScriptBody is a single fetched script (with source).
type SieveScriptBody struct {
	Name      string `json:"name"`
	Script    string `json:"script"`
	Active    bool   `json:"active"`
	UpdatedAt int64  `json:"updatedAt"`
}

// SievePutResult is the PUT response.
type SievePutResult struct {
	Name    string `json:"name"`
	Stored  bool   `json:"stored"`
	Created bool   `json:"created"`
}

// SieveCheckResult is the CHECK (dry-run compile) response. A non-compiling
// script is a successful 200 with Valid=false; Line/Col are present only for a
// SieveError (0 otherwise).
type SieveCheckResult struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message,omitempty"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
}

// SieveCapabilities advertises the interpreter's extensions and limits.
type SieveCapabilities struct {
	Implementation string   `json:"implementation"`
	Sieve          []string `json:"sieve"`
	MaxScripts     int64    `json:"maxScripts"`
	MaxScriptSize  int64    `json:"maxScriptSize"`
}

func (c *Client) sievePath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/sieve"
}

func (c *Client) sieveScriptPath(mailboxID, name string) string {
	return c.sievePath(mailboxID) + "/scripts/" + escapeSegment(name)
}

// ListSieveScripts lists a mailbox's scripts (unpaginated, no bodies).
func (c *Client) ListSieveScripts(ctx context.Context, mailboxID string) ([]SieveScript, error) {
	var out struct {
		Scripts []SieveScript `json:"scripts"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.sievePath(mailboxID) + "/scripts", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Scripts, nil
}

// GetSieveScript fetches a script's source.
func (c *Client) GetSieveScript(ctx context.Context, mailboxID, name string) (*SieveScriptBody, error) {
	var out SieveScriptBody
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.sieveScriptPath(mailboxID, name), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PutSieveScript uploads/replaces a script. 422 invalid_script (with line/col),
// 409 too_many_scripts.
func (c *Client) PutSieveScript(ctx context.Context, mailboxID, name, script string) (*SievePutResult, error) {
	var out SievePutResult
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: c.sieveScriptPath(mailboxID, name),
		body: mustJSON(map[string]string{"script": script}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSieveScript deletes a script.
func (c *Client) DeleteSieveScript(ctx context.Context, mailboxID, name string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.sieveScriptPath(mailboxID, name),
	}, nil)
}

// RenameSieveScript renames a script (body field is "newName", unlike labels).
// 409 name_exists.
func (c *Client) RenameSieveScript(ctx context.Context, mailboxID, name, newName string) error {
	return c.doJSON(ctx, request{
		method: http.MethodPost, path: c.sieveScriptPath(mailboxID, name) + "/rename",
		body: mustJSON(map[string]string{"newName": newName}), contentType: "application/json",
	}, nil)
}

// SieveActiveName returns the active script name (empty when none active).
func (c *Client) SieveActiveName(ctx context.Context, mailboxID string) (string, error) {
	var out struct {
		Name *string `json:"name"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.sievePath(mailboxID) + "/active", idempotent: true,
	}, &out)
	return derefStr(out.Name), err
}

// ActivateSieve makes a script the active filter. 404 if no such script.
func (c *Client) ActivateSieve(ctx context.Context, mailboxID, name string) error {
	return c.doJSON(ctx, request{
		method: http.MethodPut, path: c.sievePath(mailboxID) + "/active",
		body: mustJSON(map[string]string{"name": name}), contentType: "application/json",
	}, nil)
}

// DeactivateSieve clears the active filter (delivery runs unfiltered).
func (c *Client) DeactivateSieve(ctx context.Context, mailboxID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.sievePath(mailboxID) + "/active",
	}, nil)
}

// CheckSieve dry-run compiles a script without storing it. A non-compiling
// script is a 200 with Valid=false, not an error.
func (c *Client) CheckSieve(ctx context.Context, mailboxID, script string) (*SieveCheckResult, error) {
	var out SieveCheckResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.sievePath(mailboxID) + "/check",
		body: mustJSON(map[string]string{"script": script}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SieveCaps advertises supported extensions and limits.
func (c *Client) SieveCaps(ctx context.Context, mailboxID string) (*SieveCapabilities, error) {
	var out SieveCapabilities
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.sievePath(mailboxID) + "/capabilities", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
