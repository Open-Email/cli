package coreapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
)

// RawResponse is the result of an arbitrary passthrough request.
type RawResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// RawRequest performs a single arbitrary request (NO retry — the method may be
// non-idempotent) for the `openemail api` escape hatch, returning the status and
// full body regardless of status. path is relative to /api/v1 and is published
// verbatim as RawPath (the caller owns any escaping).
func (c *Client) RawRequest(ctx context.Context, method, path string, query url.Values, headers map[string]string, body []byte) (*RawResponse, error) {
	u := *c.base
	escaped := c.base.Path + "/api/v1" + path
	u.RawPath = escaped
	if dec, err := url.PathUnescape(escaped); err == nil {
		u.Path = dec
	} else {
		u.Path = escaped
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, err
	}
	return &RawResponse{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}
