package coreapi

import (
	"context"
	"fmt"
	"testing"
)

func TestDepaginateMaxPagesLimit(t *testing.T) {
	// Infinite pages
	pageCount := 0
	fetch := func(ctx context.Context, cursor string) (Page[int], error) {
		pageCount++
		return Page[int]{Items: []int{pageCount}, NextCursor: fmt.Sprintf("cursor_%d", pageCount)}, nil
	}

	maxPages := 5
	items, err := DepaginateWithLimit(context.Background(), maxPages, fetch)
	if err == nil {
		t.Fatal("expected error when exceeding maxPages, got nil")
	}
	if len(items) != maxPages {
		t.Fatalf("expected %d items before abort, got %d", maxPages, len(items))
	}
}
