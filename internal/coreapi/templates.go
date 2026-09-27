package coreapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// Tenant MAIL TEMPLATES (core docs/templated-mail-design.md §VI): stored,
// per-language messages an account's backend sends by name, with variables
// filled in per recipient. Every call is under the account the caller names;
// an account key reaches only its own, a system key names the tenant.

// MailTemplate is one template's metadata: its From, its default language,
// which languages have copy, and the variables it documents.
type MailTemplate struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// From is always set on a template this API created; nullable because the
	// column is shared with platform slot rows.
	From      *string                        `json:"from"`
	ReplyTo   *string                        `json:"replyTo"`
	Variables map[string]VariableDeclaration `json:"variables"`
	// DefaultLang is what a send gets when it asks for a language with no copy.
	DefaultLang string `json:"defaultLang"`
	// Version moves with every write that changes what a send produces (copy,
	// From, Reply-To, default language); a name or a declaration does not.
	Version   int64    `json:"version"`
	Languages []string `json:"languages"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
}

// VariableDeclaration documents one variable: what it is and a sample for the
// preview. Never a schema: a body may use what the declarations omit.
type VariableDeclaration struct {
	Description *string `json:"description,omitempty"`
	Sample      any     `json:"sample,omitempty"`
}

// MailTemplateList is GET /accounts/:id/templates.
type MailTemplateList struct {
	Templates []MailTemplate `json:"templates"`
}

// MailTemplateDetail is one template with its copy in every language.
type MailTemplateDetail struct {
	MailTemplate
	Variants []MailTemplateVariant `json:"variants"`
}

// MailTemplateVariant is one language's copy, as stored (the source, with its
// {{tags}}, not a rendering).
type MailTemplateVariant struct {
	Lang      string  `json:"lang"`
	Subject   string  `json:"subject"`
	Text      string  `json:"text"`
	HTML      *string `json:"html"`
	UpdatedAt int64   `json:"updatedAt"`
}

// MailTemplateRender is core's rendering of one language: what a send would
// produce, and a report on every variable.
type MailTemplateRender struct {
	Slug            string                 `json:"slug"`
	LangUsed        string                 `json:"langUsed"`
	TemplateVersion int64                  `json:"templateVersion"`
	Subject         string                 `json:"subject"`
	Text            string                 `json:"text"`
	HTML            *string                `json:"html"`
	Variables       []MailTemplateVariable `json:"variables"`
	// Source is "draft" when the call carried copy to preview (nothing was
	// stored), "stored" when it rendered the template's own copy.
	Source string `json:"source"`
}

// MailTemplateVariable is the render's report on one variable.
type MailTemplateVariable struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Sample      any     `json:"sample"`
	// Bodies names the fields that reference it: subject, text, html.
	Bodies           []string `json:"bodies"`
	FilledFromSample bool     `json:"filledFromSample"`
}

// MailTemplateSend is a send's answer: one row per recipient, and the batch's
// delivery id, which a retry reuses so only the failures go out again.
type MailTemplateSend struct {
	Slug            string                `json:"slug"`
	LangUsed        string                `json:"langUsed"`
	TemplateVersion int64                 `json:"templateVersion"`
	DeliveryID      string                `json:"deliveryId"`
	Results         []MailTemplateSendRow `json:"results"`
}

// MailTemplateSendRow is one recipient's outcome.
type MailTemplateSendRow struct {
	Address string `json:"address"`
	// Status is delivered, queued, filtered or failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// RetryAfterSeconds is set on the rows a temporary refusal stopped: the
	// recipient that hit it and every one not yet attempted.
	RetryAfterSeconds *int64 `json:"retryAfterSeconds,omitempty"`
	// DeliveryID is this recipient's own leg key.
	DeliveryID string `json:"deliveryId"`
	SentCopy   string `json:"sentCopy,omitempty"`
}

// TemplateCreate is the body of a create. Variables is the declarations JSON
// as the caller wrote it; core validates it (strictly) and stores it.
type TemplateCreate struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	From        string          `json:"from"`
	ReplyTo     string          `json:"replyTo,omitempty"`
	DefaultLang string          `json:"defaultLang,omitempty"`
	Variables   json.RawMessage `json:"variables,omitempty"`
}

// VariantCopy is one language's subject, text and optional HTML: the body of a
// variant PUT and of a draft. HTML nil is sent as JSON null, never omitted: a
// PUT replaces the language's copy whole, so "no HTML" has to be said.
type VariantCopy struct {
	Subject string  `json:"subject"`
	Text    string  `json:"text"`
	HTML    *string `json:"html"`
}

// VariantPutResult is a variant PUT's answer. Changed is false when the
// language already held exactly this copy: nothing was written.
type VariantPutResult struct {
	Slug    string `json:"slug"`
	Lang    string `json:"lang"`
	Stored  bool   `json:"stored"`
	Changed bool   `json:"changed"`
}

// VariantDeleteResult is a variant DELETE's answer (templates and slots).
type VariantDeleteResult struct {
	Slug    string `json:"slug"`
	Lang    string `json:"lang"`
	Removed bool   `json:"removed"`
}

// RenderRecipient fills the {{recipient.*}} built-ins of a render.
type RenderRecipient struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// TemplateRenderInput is the body of a render; every field is optional.
type TemplateRenderInput struct {
	Lang      string           `json:"lang,omitempty"`
	Vars      map[string]any   `json:"vars,omitempty"`
	Recipient *RenderRecipient `json:"recipient,omitempty"`
	// Preview fills any variable the call omits from its declared sample.
	Preview bool `json:"preview,omitempty"`
	// Draft renders this copy instead of the stored one, under the checks a
	// save makes, and stores nothing (core §VI, amended 2026-09-25).
	Draft *VariantCopy `json:"draft,omitempty"`
}

// TemplateRecipient is one addressee of a send, with values layered over the
// send's shared ones.
type TemplateRecipient struct {
	Email string         `json:"email"`
	Name  string         `json:"name,omitempty"`
	Vars  map[string]any `json:"vars,omitempty"`
}

// TemplateSendInput is the body of a send. No sendAt, no cc or bcc: core
// refuses the first and has no place for the others.
type TemplateSendInput struct {
	Lang    string              `json:"lang,omitempty"`
	Vars    map[string]any      `json:"vars,omitempty"`
	To      []TemplateRecipient `json:"to"`
	ReplyTo string              `json:"replyTo,omitempty"`
	Headers map[string]string   `json:"headers,omitempty"`
	// Save keeps a Sent copy; core's default here is FALSE, unlike /send.
	Save   *bool `json:"save,omitempty"`
	Bounce *bool `json:"bounce,omitempty"`
}

func templatesPath(accountID string) string {
	return "/accounts/" + escapeSegment(accountID) + "/templates"
}

func templatePath(accountID, slug string) string {
	return templatesPath(accountID) + "/" + escapeSegment(slug)
}

// ListTemplates returns every template on the account.
func (c *Client) ListTemplates(ctx context.Context, accountID string) ([]MailTemplate, error) {
	var out MailTemplateList
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: templatesPath(accountID), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return out.Templates, nil
}

// CreateTemplate registers one. Its From must be an address the account may
// already send as (400 from_not_sendable); the copy is written afterwards.
func (c *Client) CreateTemplate(ctx context.Context, accountID string, in TemplateCreate) (*MailTemplate, error) {
	var out MailTemplate
	if err := c.doJSON(ctx, request{method: http.MethodPost, path: templatesPath(accountID), body: mustJSON(in)}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTemplate returns one template with its copy in every language.
func (c *Client) GetTemplate(ctx context.Context, accountID, slug string) (*MailTemplateDetail, error) {
	var out MailTemplateDetail
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: templatePath(accountID, slug), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateTemplate applies a partial patch (name, from, replyTo, defaultLang,
// variables). A nil replyTo clears it. Core writes only what differs.
func (c *Client) UpdateTemplate(ctx context.Context, accountID, slug string, patch map[string]any) (*MailTemplate, error) {
	var out MailTemplate
	if err := c.doJSON(ctx, request{method: http.MethodPatch, path: templatePath(accountID, slug), body: mustJSON(patch)}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTemplate removes a template with every language's copy. A backend
// still sending it gets not_found from then on.
func (c *Client) DeleteTemplate(ctx context.Context, accountID, slug string) error {
	return c.doJSON(ctx, request{method: http.MethodDelete, path: templatePath(accountID, slug)}, nil)
}

// PutTemplateVariant writes one language's copy. It is PUBLISHED: the next
// send in that language uses it.
func (c *Client) PutTemplateVariant(ctx context.Context, accountID, slug, lang string, copy VariantCopy) (*VariantPutResult, error) {
	var out VariantPutResult
	path := templatePath(accountID, slug) + "/variants/" + escapeSegment(lang)
	if err := c.doJSON(ctx, request{method: http.MethodPut, path: path, body: mustJSON(copy), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTemplateVariant removes one language's copy.
func (c *Client) DeleteTemplateVariant(ctx context.Context, accountID, slug, lang string) (*VariantDeleteResult, error) {
	var out VariantDeleteResult
	path := templatePath(accountID, slug) + "/variants/" + escapeSegment(lang)
	if err := c.doJSON(ctx, request{method: http.MethodDelete, path: path}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenderTemplate asks core what a send would produce. It sends nothing; with
// a draft it stores nothing either.
func (c *Client) RenderTemplate(ctx context.Context, accountID, slug string, in TemplateRenderInput) (*MailTemplateRender, error) {
	var out MailTemplateRender
	path := templatePath(accountID, slug) + "/render"
	if err := c.doJSON(ctx, request{method: http.MethodPost, path: path, body: mustJSON(in), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendTemplate renders and sends one message per recipient. deliveryID is the
// batch's idempotency key: each recipient's message derives its own from it,
// so a retry with the same id converges per recipient and only the failures
// go out again. A 207 (some recipients failed) is an answer, not an error;
// the rows say which.
func (c *Client) SendTemplate(ctx context.Context, accountID, slug string, in TemplateSendInput, deliveryID string) (*MailTemplateSend, error) {
	var out MailTemplateSend
	path := templatePath(accountID, slug) + "/send"
	err := c.doJSON(ctx, request{
		method:  http.MethodPost,
		path:    path,
		body:    mustJSON(in),
		headers: map[string]string{"X-Delivery-Id": deliveryID},
		// Idempotent because the delivery id makes a replay converge.
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
