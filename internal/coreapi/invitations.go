package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// PimInvitationStatus answers whether an event with a given UID is already
// filed anywhere in the mailbox, which calendar holds it, and what the caller
// has answered so far. Deliberately cross-collection: an auto-filed or
// user-moved copy is found wherever it lives.
type PimInvitationStatus struct {
	Found      bool    `json:"found"`
	CalendarID *string `json:"calendarId"`
	Href       *string `json:"href"`
	// MyPartstat is the caller's current PARTSTAT on the stored copy, if they
	// are on it; MyAddress is which of this mailbox's addresses the event names
	// as an attendee.
	MyPartstat *string `json:"myPartstat"`
	MyAddress  *string `json:"myAddress"`
}

// PimRsvpResult is the outcome of answering an invitation from its raw
// text/calendar part. Filed reports that the event was not stored yet and a
// copy was created; OrganizerUpdated and ReplySent are the same mutually
// exclusive pair the REST RSVP returns.
type PimRsvpResult struct {
	Partstat         string  `json:"partstat"`
	CalendarID       string  `json:"calendarId"`
	Href             string  `json:"href"`
	MyAddress        string  `json:"myAddress"`
	Filed            bool    `json:"filed"`
	Etag             string  `json:"etag"`
	OrganizerUpdated bool    `json:"organizerUpdated"`
	ReplySent        bool    `json:"replySent"`
	Summary          *string `json:"summary"`
}

// InvitationStatus reports what this mailbox knows about an event UID — what a
// mail client asks before offering Accept/Tentative/Decline buttons.
func (c *Client) InvitationStatus(ctx context.Context, mailboxID, uid string) (*PimInvitationStatus, error) {
	q := url.Values{}
	q.Set("uid", uid)
	var out PimInvitationStatus
	err := c.doJSON(ctx, request{
		method: http.MethodGet,
		path:   "/mailboxes/" + escapeSegment(mailboxID) + "/calendars/invitations",
		query:  q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RespondToInvitation is the one call behind a mail client's Accept /
// Tentative / Decline buttons: hand it the raw text/calendar part and a
// PARTSTAT. It files an attendee copy into the default calendar when the event
// is not already stored, records the reply, and tells the organizer — patching
// their copy in place when they are local, mailing a METHOD:REPLY when not.
//
// 403 when no address of this mailbox is an attendee (or the caller is the
// organizer — answering your own invitation is refused).
func (c *Client) RespondToInvitation(ctx context.Context, mailboxID, ics, partstat string) (*PimRsvpResult, error) {
	var out PimRsvpResult
	err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/mailboxes/" + escapeSegment(mailboxID) + "/calendars/invitations/respond",
		body:        mustJSON(map[string]string{"ics": ics, "partstat": partstat}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
