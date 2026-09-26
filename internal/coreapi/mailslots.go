package coreapi

import (
	"context"
	"net/http"
)

// PLATFORM MAIL SLOTS (core docs/templated-mail-design.md §VII): the copy of
// every message core writes about somebody's account, overridable per
// language by an operator. A BARE system key only (not one acting for a
// mailbox); every write is audited with the operator's key as the actor.
//
// NOT PINNED by the contract test: core documents no response schema for
// these routes (they are operator-only and absent from the public document),
// so there is no component to pin them to. The fields below are the ones
// core's handlers answer today (src/endpoints/mailslots.ts).

// MailSlot is one registry entry: the contract an override is held to.
type MailSlot struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Channel is how the message leaves: email_sending (to an outside address)
	// or local (injected into the user's own mailbox).
	Channel string `json:"channel"`
	// Constraints are security rules on the copy, e.g. no_urls, text_only.
	Constraints      []string           `json:"constraints"`
	Variables        []MailSlotVariable `json:"variables"`
	DefaultLanguages []string           `json:"defaultLanguages"`
	// OverriddenLanguages is present on the listing only.
	OverriddenLanguages []string `json:"overriddenLanguages,omitempty"`
}

// MailSlotVariable is one variable the slot's call site passes. Required means
// the code always supplies it; an optional one must be guarded in the copy.
type MailSlotVariable struct {
	Name        string  `json:"name"`
	Required    bool    `json:"required"`
	Description *string `json:"description"`
	Sample      any     `json:"sample"`
}

// MailSlotDetail is one slot: its contract, the compiled-in default source per
// language, and any operator override beside it.
type MailSlotDetail struct {
	MailSlot
	Defaults  []MailSlotCopy `json:"defaults"`
	Overrides []MailSlotCopy `json:"overrides"`
}

// MailSlotCopy is one language's source (a default or an override).
type MailSlotCopy struct {
	Lang    string  `json:"lang"`
	Subject string  `json:"subject"`
	Text    string  `json:"text"`
	HTML    *string `json:"html,omitempty"`
	// UpdatedAt is set on an override only.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

// MailSlotRender is core's rendering of a slot with the registry's samples.
type MailSlotRender struct {
	Slug     string `json:"slug"`
	LangUsed string `json:"langUsed"`
	// Source is override, default or draft.
	Source    string                 `json:"source"`
	Subject   string                 `json:"subject"`
	Text      string                 `json:"text"`
	HTML      *string                `json:"html"`
	Variables []MailTemplateVariable `json:"variables"`
}

// SlotPutResult is an override PUT's answer.
type SlotPutResult struct {
	Slug   string `json:"slug"`
	Lang   string `json:"lang"`
	Stored bool   `json:"stored"`
}

// SlotRenderInput is the body of a slot preview; samples fill what Vars omits.
type SlotRenderInput struct {
	Lang  string         `json:"lang,omitempty"`
	Vars  map[string]any `json:"vars,omitempty"`
	Draft *VariantCopy   `json:"draft,omitempty"`
}

// SlotTestInput is the body of a test send. No values: a test renders the
// registry's samples only (core refuses `vars`), so it cannot become a mailer
// for arbitrary text on the platform's own sending identity.
type SlotTestInput struct {
	To    string       `json:"to"`
	Lang  string       `json:"lang,omitempty"`
	Draft *VariantCopy `json:"draft,omitempty"`
}

// SlotTestResult is a test send's answer.
type SlotTestResult struct {
	Slug     string `json:"slug"`
	LangUsed string `json:"langUsed"`
	Sent     bool   `json:"sent"`
}

func slotPath(slug string) string { return "/system/mail-slots/" + escapeSegment(slug) }

// ListMailSlots returns the registry joined to the overrides that exist.
func (c *Client) ListMailSlots(ctx context.Context) ([]MailSlot, error) {
	var out struct {
		Slots []MailSlot `json:"slots"`
	}
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: "/system/mail-slots", idempotent: true}, &out); err != nil {
		return nil, err
	}
	return out.Slots, nil
}

// GetMailSlot returns one slot, its defaults and its overrides.
func (c *Client) GetMailSlot(ctx context.Context, slug string) (*MailSlotDetail, error) {
	var out MailSlotDetail
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: slotPath(slug), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutMailSlotVariant overrides one language. Core holds it to the slot's
// contract first: a link in a code mail, say, is refused and never stored.
func (c *Client) PutMailSlotVariant(ctx context.Context, slug, lang string, copy VariantCopy) (*SlotPutResult, error) {
	var out SlotPutResult
	path := slotPath(slug) + "/variants/" + escapeSegment(lang)
	if err := c.doJSON(ctx, request{method: http.MethodPut, path: path, body: mustJSON(copy), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMailSlotVariant removes an override: that language goes back to the
// compiled-in default.
func (c *Client) DeleteMailSlotVariant(ctx context.Context, slug, lang string) (*VariantDeleteResult, error) {
	var out VariantDeleteResult
	path := slotPath(slug) + "/variants/" + escapeSegment(lang)
	if err := c.doJSON(ctx, request{method: http.MethodDelete, path: path}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenderMailSlot previews a slot (or a draft of it) with the registry's samples.
func (c *Client) RenderMailSlot(ctx context.Context, slug string, in SlotRenderInput) (*MailSlotRender, error) {
	var out MailSlotRender
	if err := c.doJSON(ctx, request{method: http.MethodPost, path: slotPath(slug) + "/render", body: mustJSON(in), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TestMailSlot sends one real copy to the address given. Only Email Sending
// slots can be tested (400 slot_channel_not_testable otherwise); a refusal by
// the sender is a 502 naming which half refused and why.
func (c *Client) TestMailSlot(ctx context.Context, slug string, in SlotTestInput) (*SlotTestResult, error) {
	var out SlotTestResult
	if err := c.doJSON(ctx, request{method: http.MethodPost, path: slotPath(slug) + "/test", body: mustJSON(in)}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
