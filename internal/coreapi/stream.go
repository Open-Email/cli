package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// StreamGet performs a GET whose body is consumed as a stream (e.g. a raw
// message/rfc822 download). The returned ReadCloser MUST be closed by the caller;
// closing it releases the underlying request context. GETs are idempotent, so the
// call is retried on transport/5xx per the retry policy before any body is handed
// back. path segments must already be escapeSegment'd.
func (c *Client) StreamGet(ctx context.Context, path string, query url.Values) (io.ReadCloser, error) {
	body, _, err := c.StreamGetResp(ctx, path, query)
	return body, err
}

// StreamGetResp is StreamGet that also returns the response headers, for a
// caller that needs Content-Type / Content-Disposition / Content-Length (e.g. to
// name and describe a downloaded MIME part). The ReadCloser MUST be closed.
func (c *Client) StreamGetResp(ctx context.Context, path string, query url.Values) (io.ReadCloser, http.Header, error) {
	resp, err := c.do(ctx, request{
		method:       http.MethodGet,
		path:         path,
		query:        query,
		idempotent:   true,
		streamResult: true,
	})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp.Header, nil
}
