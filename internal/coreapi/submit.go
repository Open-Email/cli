package coreapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Sending a draft that is ALREADY STORED, byte-for-byte as stored.
//
// This is not /send with the fields filled in from a draft: core reads the
// stored message, derives the envelope from its own headers, strips Bcc from
// the wire copy, and turns the draft ROW into the Sent copy — no rebuild, no
// attachment round trip, no second copy. So what the recipient receives is
// exactly what was reviewed, which is the property a composer wants and a
// rebuild cannot promise.

// ScheduledLegResult is one recipient's outcome inside a scheduled or pending
// submission.
type ScheduledLegResult struct {
	Address    string `json:"address"`
	Status     string `json:"status"`
	DeliveryID string `json:"deliveryId"`
	Error      string `json:"error,omitempty"`
}

// ScheduledSend is a submission core is still working on. SubmitDraft answers
// one at 202: a leg hit something retryable, so core owns the retry from here
// and the caller must NOT re-submit — the draft is claimed, and a second
// submission under a new delivery id is how one message gets sent twice.
type ScheduledSend struct {
	ScheduleID string  `json:"scheduleId"`
	DeliveryID string  `json:"deliveryId"`
	MessageID  string  `json:"messageId"`
	ThreadID   *string `json:"threadId"`
	// SendAt is when it was due, ReleaseAt when the release actually ran, both
	// epoch seconds. For a draft submitted now they are the same instant.
	SendAt     int64                `json:"sendAt"`
	ReleaseAt  int64                `json:"releaseAt"`
	State      string               `json:"state"`
	Recipients []string             `json:"recipients"`
	Attempts   int                  `json:"attempts"`
	Deferrals  int                  `json:"deferrals"`
	CreatedAt  int64                `json:"createdAt"`
	FinishedAt *int64               `json:"finishedAt"`
	Result     []ScheduledLegResult `json:"result"`
	Error      *string              `json:"error"`
}

// SubmitOptions are the knobs on submitting a stored draft. There is
// deliberately no save flag — the draft row itself becomes the Sent copy, so
// there is no second copy to keep or skip.
type SubmitOptions struct {
	// Bounce opts OUT of the DSN core would otherwise deliver here if the relay
	// fails terminally (nil = core's default, true).
	Bounce *bool
	// DeliveryID is the idempotency key. A replay answers 202 with the
	// submission's own record rather than sending again, which is the only thing
	// standing between a retried command and a second copy in the recipient's
	// inbox.
	DeliveryID string
}

// SubmitResult is one of two shapes, chosen by the status code core answered
// with. Exactly one field is non-nil.
type SubmitResult struct {
	// Sent is the per-recipient outcome (200 all accepted, 207 mixed).
	Sent *SendResult
	// Pending means core accepted the submission and is retrying it (202). It is
	// also what a replay of a spent delivery id answers.
	Pending *ScheduledSend
	// Status is the HTTP status core answered with.
	Status int
}

// SubmitDraft sends a message already stored in this mailbox as a draft.
//
// Every RFC 5322 From address in the stored bytes must be this mailbox's own —
// a draft's headers are client-written, so core re-earns the right to send
// under them at submission (403 from_not_owned / from_sending_disabled) rather
// than trusting what was filed.
//
// The message must still BE a draft: core pins that, and "delete" is a Trash
// label move everywhere in this platform, so a draft moved to Trash between
// composing and submitting is refused (409) rather than sent out of the bin.
//
// A retryable per-recipient outcome comes back as Pending, not as a failure —
// see SubmitResult.
func (c *Client) SubmitDraft(ctx context.Context, mailboxID, messageID string, opts SubmitOptions) (*SubmitResult, []byte, error) {
	r := request{
		method: http.MethodPost,
		path: "/mailboxes/" + escapeSegment(mailboxID) + "/messages/" +
			escapeSegment(messageID) + "/submit",
	}
	q := url.Values{}
	strictBool(q, "bounce", opts.Bounce)
	if len(q) > 0 {
		r.query = q
	}
	if opts.DeliveryID != "" {
		r.headers = map[string]string{"X-Delivery-Id": opts.DeliveryID}
		// A keyed submission is safe to replay: core absorbs the repeat into the
		// same submission rather than sending twice.
		r.idempotent = true
	}

	// The status selects the shape, so decode only after seeing it.
	raw, status, err := c.doJSONStatus(ctx, r, nil)
	if err != nil {
		return nil, raw, err
	}
	out := &SubmitResult{Status: status}
	decode := func(v any) error {
		if len(raw) == 0 {
			return nil
		}
		if uerr := json.Unmarshal(raw, v); uerr != nil {
			return fmt.Errorf("coreapi: decode %s %s: %w", r.method, r.path, uerr)
		}
		return nil
	}
	if status == http.StatusAccepted {
		var pending ScheduledSend
		if derr := decode(&pending); derr != nil {
			return nil, raw, derr
		}
		out.Pending = &pending
		return out, raw, nil
	}
	var sent SendResult
	if derr := decode(&sent); derr != nil {
		return nil, raw, derr
	}
	out.Sent = &sent
	return out, raw, nil
}
