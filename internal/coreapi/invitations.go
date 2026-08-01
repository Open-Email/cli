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

// MessageInvitation is the scheduling part a message carries, parsed, merged
// with what this mailbox has already filed and answered. It is the ONE call
// behind an RSVP banner: locating the part, reading RFC 5545 out of it, and
// resolving the reader's own standing on the event are all server-side, so a
// client neither hand-parses iCalendar nor guesses whether the buttons it draws
// would be accepted.
//
// Two fields carry judgements a client cannot make for itself:
//
//   - CanRespond is the respond endpoint's OWN acceptance predicate. Render the
//     buttons if and only if it is true.
//   - IsOrganizer is resolved on any routing tier, so an alias or catch-all that
//     lands on this mailbox still counts. An organizer answers by editing their
//     own copy; the respond endpoint refuses them (403 is_organizer), because
//     filing an ICS that names you as organizer would schedule iTIP invitations
//     on your behalf to addresses the sender chose.
//
// MyPartstat prefers the STORED copy when Found — the reader may have answered
// since the mail arrived — and falls back to what the invitation itself records.
type MessageInvitation struct {
	// Section is the MIME section of the text/calendar part; pass it back when
	// responding so the server answers the part actually shown.
	Section string  `json:"section"`
	Method  *string `json:"method"` // REQUEST | CANCEL | REPLY | …; null for a non-scheduling body
	UID     *string `json:"uid"`
	// Component is VEVENT | VTODO | VJOURNAL.
	Component *string `json:"component"`
	Summary   *string `json:"summary"`
	Location  *string `json:"location"`
	// DTStart/DTEnd are epoch seconds, already resolved against the object's
	// timezone — no VTIMEZONE to interpret client-side.
	DTStart *int64  `json:"dtstart"`
	DTEnd   *int64  `json:"dtend"`
	RRule   *string `json:"rrule"`

	Organizer *string `json:"organizer"`
	// Attendees is truncated to the iTIP fan-out bound; AttendeeCount is the
	// real total.
	Attendees     []PimAttendee `json:"attendees"`
	AttendeeCount int64         `json:"attendeeCount"`

	MyAddress   *string `json:"myAddress"`
	MyPartstat  *string `json:"myPartstat"`
	IsOrganizer bool    `json:"isOrganizer"`
	CanRespond  bool    `json:"canRespond"`

	Found      bool    `json:"found"`
	CalendarID *string `json:"calendarId"`
	Href       *string `json:"href"`
}

// GetMessageInvitation reads the invitation a message carries. It resolves a
// TRASHED message too, exactly as /content and /parts do, so a banner renders
// inside any message the reader can open.
//
// `404 no_invitation` is the ORDINARY answer for ordinary mail, not a failure —
// test it with IsNotFound and simply show no banner.
func (c *Client) GetMessageInvitation(ctx context.Context, mailboxID, messageID string) (*MessageInvitation, error) {
	var out MessageInvitation
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       c.messagePath(mailboxID, messageID) + "/invitation",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RespondToMessageInvitation answers the invitation carried by a message.
//
// Prefer this over RespondToInvitation: the bytes are already on the server, so
// the inline form only makes a client fetch the part, decode its charset, and
// post it straight back. section is optional — omitted, the server picks the
// message's scheduling part itself.
func (c *Client) RespondToMessageInvitation(ctx context.Context, mailboxID, messageID, section, partstat string) (*PimRsvpResult, error) {
	body := map[string]string{"messageId": messageID, "partstat": partstat}
	if section != "" {
		body["section"] = section
	}
	var out PimRsvpResult
	err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/mailboxes/" + escapeSegment(mailboxID) + "/calendars/invitations/respond",
		body:        mustJSON(body),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
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
