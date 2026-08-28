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
	// Participants are the distinct senders in the conversation, oldest first,
	// deduplicated by address — the "Carol, Alice, Bob" line a conversation row
	// shows. Conversation-wide, which is why the exemplar alone cannot supply
	// it: the exemplar is one message and this describes all of them.
	Participants []ThreadParticipant `json:"participants"`
}

// ThreadParticipant is one distinct sender in a conversation.
//
// Name is a POINTER because the wire contract makes it required AND nullable:
// the address is always known, the display name often is not, and null is how
// core says so. A plain string would decode a null to "" and lose the
// distinction between "no name" and "named empty".
type ThreadParticipant struct {
	Email string  `json:"email"`
	Name  *string `json:"name"`
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
// merged away and was recovered. NextCursor is the member-page continuation —
// present only while more pages exist.
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
// rather than the newest-arrived one). The next-page cursor rides the
// `nextCursor` field — absent once the listing is exhausted.
func (c *Client) ListThreads(ctx context.Context, mailboxID string, opts ListOpts) (Page[ThreadListItem], error) {
	q := opts.values(opts.Cursor)
	var out struct {
		Threads    []ThreadListItem `json:"threads"`
		NextCursor string           `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.threadsPath(mailboxID), query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[ThreadListItem]{}, err
	}
	return Page[ThreadListItem]{Items: out.Threads, NextCursor: out.NextCursor}, nil
}

// GetThread returns one thread's messages (oldest-first). Members paginate
// ascending via the response's `nextCursor` field.
func (c *Client) GetThread(ctx context.Context, mailboxID, threadID string, limit int, cursor string) (*ThreadView, string, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out ThreadView
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.threadsPath(mailboxID) + "/" + escapeSegment(threadID),
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, "", err
	}
	return &out, derefStr(out.NextCursor), nil
}
