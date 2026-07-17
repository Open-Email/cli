package coreapi

import (
	"context"
	"net/http"
	"time"
)

// Health probes GET /api/v1/health (no auth required). Returns nil iff core
// answers 2xx.
func (c *Client) Health(ctx context.Context) error {
	return c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/health",
		idempotent: true,
		timeout:    10 * time.Second,
	}, nil)
}
