package coreapi

import (
	"context"
	"fmt"
	"net/url"
)

// Page is one page of a cursor-paginated list. NextCursor is empty on the last
// page (core omits the field, not sends null).
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// pageValues builds the shared limit/cursor query fragment.
func pageValues(limit int, cursor string) url.Values {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q
}

// Depaginate drains every page by following NextCursor. Used by list commands'
// --all flag; the rate cost is the user's explicit choice.
func Depaginate[T any](ctx context.Context, fetch func(ctx context.Context, cursor string) (Page[T], error)) ([]T, error) {
	var all []T
	cursor := ""
	for {
		pg, err := fetch(ctx, cursor)
		if err != nil {
			return all, err
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == "" {
			return all, nil
		}
		// Stall guard: a cursor that fails to advance would loop forever and
		// accumulate unboundedly. Bail rather than hang.
		if pg.NextCursor == cursor {
			return all, fmt.Errorf("coreapi: pagination cursor did not advance (%q) — aborting", cursor)
		}
		cursor = pg.NextCursor
	}
}
