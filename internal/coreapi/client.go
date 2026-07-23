// Package coreapi is the typed HTTP client for the openemail-core REST API used
// by the openemail CLI. It carries an arbitrary bearer (a system, account, or
// mailbox principal — set per invocation from the resolved profile), pools
// connections, and retries per core's documented error vocabulary (503
// superseded/reaped always; transport and 502/504 for idempotent calls).
//
// The transport kernel — request descriptor + single do(), APIError decode with
// Is* predicates, RawPath escaping, buffered-body-before-cancel, re-openable
// streaming bodies — is adapted from the gateway's internal/coreapi, minus the
// per-mailbox gate, circuit breaker, and Observer a short-lived CLI process does
// not need.
package coreapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config configures the client.
type Config struct {
	BaseURL         string // deployment root, e.g. https://api.open.email
	Token           string // bearer (oek_… or oemp_…); empty = unauthenticated
	UserAgent       string // e.g. openemail-cli/0.1.0
	RequestTimeout  time.Duration
	RetryAttempts   int
	RetryBackoffMin time.Duration
	RetryBackoffMax time.Duration
	HTTPClient      *http.Client // optional override (tests)
}

func (c Config) withDefaults() Config {
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = 3
	}
	if c.RetryBackoffMin <= 0 {
		c.RetryBackoffMin = 200 * time.Millisecond
	}
	if c.RetryBackoffMax <= 0 {
		c.RetryBackoffMax = 5 * time.Second
	}
	if c.UserAgent == "" {
		c.UserAgent = "openemail-cli"
	}
	return c
}

// maxJSONBody bounds buffered JSON response bodies (a safety cap).
const maxJSONBody = 64 << 20

// Client is safe for concurrent use.
type Client struct {
	cfg    Config
	base   *url.URL
	hc     *http.Client
	retryP retryPolicy
}

// New builds a Client. It fails only if BaseURL is unparseable/relative; an empty
// Token is allowed (login probes and unauthenticated routes like /health and
// /openapi.json need no bearer).
func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("coreapi: bad base url %q: %w", cfg.BaseURL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("coreapi: base url must be absolute, got %q", cfg.BaseURL)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		}} // per-request timeouts via context, not http.Client.Timeout
	}
	return &Client{
		cfg:    cfg,
		base:   base,
		hc:     hc,
		retryP: retryPolicy{attempts: cfg.RetryAttempts, backoffMin: cfg.RetryBackoffMin, backoffMax: cfg.RetryBackoffMax},
	}, nil
}

// BaseURL returns the configured deployment root.
func (c *Client) BaseURL() string { return c.base.String() }

// request describes one HTTP call to core (path is relative to /api/v1).
type request struct {
	method string
	path   string // e.g. "/mailboxes/ID/labels"; segments already url.PathEscape'd
	query  url.Values
	body   []byte // JSON body (re-readable across retries)
	// getBody yields a fresh reader per attempt (streaming uploads: send/append),
	// so retries never replay a consumed reader. Mutually exclusive with body.
	getBody     func() (io.ReadCloser, error)
	bodyLen     int64 // Content-Length for streaming
	contentType string
	headers     map[string]string
	timeout     time.Duration // 0 → RequestTimeout
	// idempotent widens retry to transport errors + 502/504. GETs and calls made
	// idempotent by an X-Delivery-Id set it; unsafe mutations do not.
	idempotent   bool
	streamResult bool // caller consumes resp.Body; do() returns it open
}

// do executes a request with retry, returning the raw response for the caller to
// decode. On non-2xx it reads the body and returns an *APIError. On a 2xx with
// streamResult it returns the response with Body still open (context alive until
// Close).
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	u := *c.base
	// r.path segments are already percent-escaped; publish as RawPath so
	// URL.String() emits them verbatim. Assigning Path alone would re-escape every
	// '%', double-encoding any address/label/id containing '@', '+', space, '%',
	// or non-ASCII and hitting the wrong resource.
	escaped := c.base.Path + "/api/v1" + r.path
	u.RawPath = escaped
	if dec, err := url.PathUnescape(escaped); err == nil {
		u.Path = dec
	} else {
		u.Path = escaped
	}
	if len(r.query) > 0 {
		u.RawQuery = r.query.Encode()
	}

	timeout := r.timeout
	if timeout <= 0 {
		timeout = c.cfg.RequestTimeout
	}

	var resp *http.Response
	err := c.retryP.do(ctx, func(attempt int) (bool, error) {
		// A streaming download (streamResult) and a streaming upload (getBody) are
		// both bounded by the caller's ctx, not our per-request timeout: a fixed
		// deadline would abort a large transfer partway through purely because it is
		// big — a 50 MB message on a slow uplink can need far longer than the 30s
		// default, and being idempotent it would then retry and fail the same way.
		// Ordinary calls have a small/absent request body and buffer the response
		// within do(), so the timeout correctly spans the whole exchange.
		var reqCtx context.Context
		var cancel context.CancelFunc
		if r.streamResult || r.getBody != nil {
			reqCtx, cancel = context.WithCancel(ctx)
		} else {
			reqCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		var bodyReader io.Reader
		if r.getBody != nil {
			rc, e := r.getBody()
			if e != nil {
				cancel()
				return false, e
			}
			bodyReader = rc
		} else if r.body != nil {
			bodyReader = bytes.NewReader(r.body)
		}
		httpReq, e := http.NewRequestWithContext(reqCtx, r.method, u.String(), bodyReader)
		if e != nil {
			cancel()
			return false, e
		}
		if c.cfg.Token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		httpReq.Header.Set("User-Agent", c.cfg.UserAgent)
		httpReq.Header.Set("Accept", "application/json")
		if r.contentType != "" {
			httpReq.Header.Set("Content-Type", r.contentType)
		}
		if r.getBody != nil {
			httpReq.ContentLength = r.bodyLen
		}
		for k, v := range r.headers {
			httpReq.Header.Set(k, v)
		}

		hr, e := c.hc.Do(httpReq)
		if e != nil {
			cancel()
			// Transport error: retry for idempotent calls (and streaming uploads,
			// idempotent via delivery id).
			return r.idempotent || r.getBody != nil, e
		}

		if hr.StatusCode >= 200 && hr.StatusCode < 300 {
			if r.streamResult {
				hr.Body = &cancelBody{ReadCloser: hr.Body, cancel: cancel}
			} else {
				// Buffer while the request context is alive, then cancel — cancelling
				// first would abort the caller's later read of a large response.
				body, readErr := io.ReadAll(io.LimitReader(hr.Body, maxJSONBody))
				hr.Body.Close()
				cancel()
				if readErr != nil {
					return r.idempotent || r.getBody != nil, readErr
				}
				hr.Body = io.NopCloser(bytes.NewReader(body))
			}
			resp = hr
			return false, nil
		}

		// Non-2xx: read (bounded) body, decode error, decide retry.
		data, _ := io.ReadAll(io.LimitReader(hr.Body, 256<<10))
		hr.Body.Close()
		cancel()
		ae := decodeAPIError(hr.StatusCode, data)
		if shouldRetryStatus(hr.StatusCode, r) {
			return true, ae
		}
		return false, ae
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// doJSON runs a request expecting a JSON body and decodes into out (may be nil).
func (c *Client) doJSON(ctx context.Context, r request, out any) error {
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty 2xx body — nothing to decode, not an error
		}
		return fmt.Errorf("coreapi: decode %s %s: %w", r.method, r.path, err)
	}
	return nil
}

// doJSONRaw runs a request, decodes into out (may be nil), AND returns the raw
// response body. Used by delivery/append commands so `--json` can echo core's
// exact response (union shapes with real nulls and duplicate:false) rather than a
// re-marshaled, omitempty-lossy struct.
func (c *Client) doJSONRaw(ctx context.Context, r request, out any) ([]byte, error) {
	resp, err := c.do(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, fmt.Errorf("coreapi: read %s %s: %w", r.method, r.path, err)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return data, fmt.Errorf("coreapi: decode %s %s: %w", r.method, r.path, err)
		}
	}
	return data, nil
}

// cancelBody ties context cancel to Body.Close for streamed responses.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// shouldRetryStatus implements the retry classification. 502/503/504 are all
// temporary, but only retried for calls that are safe to replay: GETs
// (idempotent), DELETEs made idempotent explicitly, or uploads carrying an
// X-Delivery-Id (getBody). A non-idempotent POST create is NOT retried — a 503
// injected by the edge *after* the write committed (Workers overload, "no
// healthy origin") would otherwise mint a duplicate resource, since directory
// creates carry no idempotency key. (Core's own delivery path answers 503
// superseded/reaped only for calls that carry a delivery id and provably did not
// commit; those set getBody/idempotent and do retry.)
func shouldRetryStatus(status int, r request) bool {
	switch status {
	case 502, 503, 504:
		return r.idempotent || r.getBody != nil
	}
	return false
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("coreapi: marshal: %v", err))
	}
	return b
}

func itoa(n int) string { return strconv.Itoa(n) }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// derefStr flattens a nullable JSON string (a *string field) to "" when null.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
