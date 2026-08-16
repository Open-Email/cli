package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// ThreadListItem is one conversation in the thread listing.
type ThreadListItem struct {
	ThreadID       string      `json:"threadId"`
	MessageCount   int64       `json:"messageCount"`
	UnseenCount    int64       `json:"unseenCount"`
	LastReceivedAt int64       `json:"lastReceivedAt"`
	Exemplar       MessageMeta `json:"exemplar"`
}

// ReplyContext is the pre-computed reply scaffolding for a thread.
type ReplyContext struct {
	InReplyTo  *string  `json:"inReplyTo"`
	References []string `json:"references"`
	Subject    *string  `json:"subject"`
	To         *string  `json:"to"`
	// InboundSourceID is the message this context answers, or nil when the
	// thread's newest member is your own Sent copy (a follow-up to your own
	// mail). A client can use it to show or open the exact message replied to.
	InboundSourceID *string `json:"inboundSourceId"`
}

// ThreadView is a single thread with its messages (oldest-first) and reply
// context. CanonicalThreadID is present only when the requested id had been
// merged away and was recovered. NextCursor mirrors the top-level `cursor`
// the anonymous GetThread wrapper decodes — present only while more pages
// exist.
type ThreadView struct {
	ThreadID          string        `json:"threadId"`
	CanonicalThreadID *string       `json:"canonicalThreadId,omitempty"`
	Messages          []MessageMeta `json:"messages"`
	NextCursor        *string       `json:"nextCursor,omitempty"`
	MessageCount      int64         `json:"messageCount"`
	ReplyContext      *ReplyContext `json:"replyContext"`
}

func (c *Client) threadsPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/threads"
}

// ListThreads returns one page of threads (newest activity first; under
// SortBy "date" that activity — and the exemplar — is the newest-DATED member
// rather than the newest-arrived one). The next-page cursor rides the `cursor`
// field (string|null), not `nextCursor`.
func (c *Client) ListThreads(ctx context.Context, mailboxID string, opts ListOpts) (Page[ThreadListItem], error) {
	q := opts.values(opts.Cursor)
	var out struct {
		Threads []ThreadListItem `json:"threads"`
		Cursor  *string          `json:"cursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.threadsPath(mailboxID), query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[ThreadListItem]{}, err
	}
	return Page[ThreadListItem]{Items: out.Threads, NextCursor: derefStr(out.Cursor)}, nil
}

// GetThread returns one thread's messages (oldest-first). Members paginate
// ascending via the `cursor` field.
func (c *Client) GetThread(ctx context.Context, mailboxID, threadID string, limit int, cursor string) (*ThreadView, string, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out struct {
		ThreadView
		Cursor *string `json:"cursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.threadsPath(mailboxID) + "/" + escapeSegment(threadID),
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, "", err
	}
	tv := out.ThreadView
	return &tv, derefStr(out.Cursor), nil
}
