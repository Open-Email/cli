package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// LabelInfo is one row of the label listing. A label is a system label iff Role
// is non-nil (roles: inbox, sent, drafts, trash, archive).
type LabelInfo struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Role         *string `json:"role"`
	UIDValidity  int64   `json:"uidValidity"`
	UIDNext      int64   `json:"uidNext"`
	MessageCount int64   `json:"messageCount"`
	UnseenCount  int64   `json:"unseenCount"`
}

// CreatedLabel is the (smaller) create response.
type CreatedLabel struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	UIDValidity int64  `json:"uidValidity"`
}

func (c *Client) labelsPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/labels"
}

func (c *Client) labelPath(mailboxID, label string) string {
	return c.labelsPath(mailboxID) + "/" + escapeSegment(label)
}

// ListLabels returns all labels with counts and UID state (unpaginated).
func (c *Client) ListLabels(ctx context.Context, mailboxID string) ([]LabelInfo, error) {
	var out struct {
		Labels []LabelInfo `json:"labels"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.labelsPath(mailboxID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Labels, nil
}

// CreateLabel creates a user label. 409 label_exists on collision.
func (c *Client) CreateLabel(ctx context.Context, mailboxID, name string) (*CreatedLabel, error) {
	var out CreatedLabel
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.labelsPath(mailboxID),
		body: mustJSON(map[string]string{"name": name}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RenameLabel renames a user label. The body field is "name" (the new name).
// 403 system_label, 409 exists.
func (c *Client) RenameLabel(ctx context.Context, mailboxID, label, newName string) error {
	return c.doJSON(ctx, request{
		method: http.MethodPatch, path: c.labelPath(mailboxID, label),
		body: mustJSON(map[string]string{"name": newName}), contentType: "application/json",
	}, nil)
}

// DeleteLabel deletes a user label (messages keep their other labels). 403
// system_label.
func (c *Client) DeleteLabel(ctx context.Context, mailboxID, label string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.labelPath(mailboxID, label),
	}, nil)
}

// ExpungedUID is one (uid, messageId) pair detached by a label expunge (IMAP
// VANISHED-shaped). Core emits objects here, not bare UIDs.
type ExpungedUID struct {
	UID       int64  `json:"uid"`
	MessageID string `json:"messageId"`
}

// ExpungeLabel detaches \Deleted-flagged messages from a label; a message left
// with no labels moves to trash. Returns the detached (uid, messageId) pairs and
// the count of messages that moved to trash.
func (c *Client) ExpungeLabel(ctx context.Context, mailboxID, label string) (expunged []ExpungedUID, messagesExpunged int64, err error) {
	var out struct {
		Expunged         []ExpungedUID `json:"expunged"`
		MessagesExpunged int64         `json:"messagesExpunged"`
	}
	err = c.doJSON(ctx, request{
		method: http.MethodPost, path: c.labelPath(mailboxID, label) + "/expunge",
	}, &out)
	return out.Expunged, out.MessagesExpunged, err
}

// LabelMessages lists a label's messages UID-ascending within an optional UID
// range (IMAP UID FETCH). No cursor pagination — page forward by setting
// uidMin=lastUid+1. Each message carries a top-level uid within this label.
func (c *Client) LabelMessages(ctx context.Context, mailboxID, label string, uidMin, uidMax, limit int64) (msgs []MessageMeta, uidValidity int64, err error) {
	q := url.Values{}
	if uidMin > 0 {
		q.Set("uidMin", itoa64(uidMin))
	}
	if uidMax > 0 {
		q.Set("uidMax", itoa64(uidMax))
	}
	if limit > 0 {
		q.Set("limit", itoa64(limit))
	}
	var out struct {
		Messages    []MessageMeta `json:"messages"`
		UIDValidity int64         `json:"uidValidity"`
	}
	err = c.doJSON(ctx, request{
		method: http.MethodGet, path: c.labelPath(mailboxID, label) + "/messages",
		query: q, idempotent: true,
	}, &out)
	return out.Messages, out.UIDValidity, err
}
