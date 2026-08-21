package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// PimKind selects one of the two PIM path families. Core serves calendars and
// addressbooks from one implementation over two path spellings; this client
// mirrors that with one set of wrappers parameterized by kind.
type PimKind string

const (
	PimCalendars    PimKind = "calendars"
	PimAddressbooks PimKind = "addressbooks"
)

// PimScope names the mailbox whose store a PIM call addresses. MailboxID is the
// collection OWNER (the path segment). Acting, when set and different from
// MailboxID, is sent as X-Acting-Mailbox — how an account or system principal
// reaches a collection another mailbox shared with Acting. Core enforces both
// the share grant and the owner's visibility; the header is never needed when
// operating on one's own store.
type PimScope struct {
	MailboxID string
	Acting    string
}

func (s PimScope) headers() map[string]string {
	if s.Acting != "" && s.Acting != s.MailboxID {
		return map[string]string{"X-Acting-Mailbox": s.Acting}
	}
	return nil
}

// PimCollection is one calendar or addressbook.
type PimCollection struct {
	ID          string  `json:"id"`   // ULID — stable across renames
	Kind        string  `json:"kind"` // calendar | addressbook
	Name        string  `json:"name"` // URL slug (the DAV path segment)
	DisplayName *string `json:"displayName"`
	Color       *string `json:"color"`
	Description *string `json:"description"`
	Visibility  string  `json:"visibility"` // private | shared | public
	Role        *string `json:"role"`       // default | tasks | nil
	// SyncToken is opaque (DAV ctag) — relay it, never parse it.
	SyncToken   string `json:"syncToken"`
	CreatedAt   int64  `json:"createdAt"`
	ObjectCount int64  `json:"objectCount"`
}

// PimAttendee is one parsed ATTENDEE of a calendar object.
type PimAttendee struct {
	Email    string `json:"email"`
	CN       string `json:"cn,omitempty"`
	Partstat string `json:"partstat,omitempty"` // NEEDS-ACTION | ACCEPTED | DECLINED | TENTATIVE
	Role     string `json:"role,omitempty"`
	Rsvp     bool   `json:"rsvp,omitempty"`
}

// PimObjectMeta is the denormalized metadata of one stored object (an iCalendar
// or vCard resource). The vcard* extracts are nil for calendar objects and the
// scheduling fields nil for contacts.
type PimObjectMeta struct {
	ID          string  `json:"id"`
	Href        string  `json:"href"` // client-chosen resource name
	UID         string  `json:"uid"`
	Etag        string  `json:"etag"` // sha256 of the body, hex
	Size        int64   `json:"size"`
	ContentType string  `json:"contentType"`
	Component   *string `json:"component"` // VEVENT | VTODO | VJOURNAL | VCARD
	Dtstart     *int64  `json:"dtstart"`
	Dtend       *int64  `json:"dtend"`
	// Rrule is the RRULE value; the literal "RDATE" marks a series recurring by
	// RDATE only.
	Rrule       *string       `json:"rrule"`
	Organizer   *string       `json:"organizer"`
	Attendees   []PimAttendee `json:"attendees"`
	Sequence    int64         `json:"sequence"`
	EventStatus *string       `json:"eventStatus"`
	Transp      *string       `json:"transp"`
	VcardFn     *string       `json:"vcardFn"`
	VcardEmail  *string       `json:"vcardEmail"`
	VcardN      *string       `json:"vcardN"`
	CreatedAt   int64         `json:"createdAt"`
	UpdatedAt   int64         `json:"updatedAt"`
}

// PimObject is a listed object: metadata plus, unless fields=meta, the raw wire
// text, and — on ?expand=true range queries — the expanded occurrences.
// With fields=json each row also carries Data (see PimObjectJSON).
type PimObject struct {
	PimObjectMeta
	Content   string         `json:"content,omitempty"`
	Instances []PimInstance  `json:"instances,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// PimObjectJSON is the JSON representation of one object: a JSCalendar Event
// or Task (RFC 8984) or a JSContact Card (RFC 9553) instead of wire text.
//
// THE BODY decides which — never the caller. A VTODO reads back as a Task
// (`"@type":"Task"`, with `due`/`progress`/`percentComplete`/`priority` where
// an Event has `start`/`duration`/`status`), a VEVENT as an Event, a VCARD as
// a Card. Reading a to-do as an Event-shaped document is exactly the mistake
// that would drop `due` on a read-modify-write.
//
// Data is null only when the object has no mapping at all — a VJOURNAL (no
// JSCalendar type models one) or a body that could not be parsed — and Content
// carries the wire text in exactly that case.
//
// Writable is false when the object converts TO JSON but cannot be converted
// back: the reader is more permissive than the writer (a VEVENT with no
// DTSTART, or a VTODO carrying DURATION without DTSTART, reads fine and could
// never be written), so this says up front that an edit would be refused.
type PimObjectJSON struct {
	PimObjectMeta
	Data     map[string]any `json:"data"`
	Writable bool           `json:"writable"`
	Content  string         `json:"content,omitempty"`
}

// PimInstance is one expanded occurrence of a recurring calendar object.
type PimInstance struct {
	Start        int64   `json:"start"`
	End          int64   `json:"end"` // exclusive
	RecurrenceID *int64  `json:"recurrenceId"`
	Transp       *string `json:"transp"`
	Status       *string `json:"status"`
	// Component is the occurrence as a standalone VCALENDAR, RECURRENCE-ID stamped.
	Component string `json:"component"`
}

// PimWindow echoes the effective range a ranged query actually answered.
type PimWindow struct {
	Start   int64 `json:"start"`
	End     int64 `json:"end"`
	Clamped bool  `json:"clamped"`
}

// PimObjectPage is one page of an objects listing / range query.
type PimObjectPage struct {
	Objects    []PimObject `json:"objects"`
	NextCursor string      `json:"nextCursor,omitempty"`
	Window     *PimWindow  `json:"window,omitempty"`
}

// PimChanges is the sync diff since an opaque token (RFC 6578 shaped).
type PimChanges struct {
	SyncToken string          `json:"syncToken"`
	Changed   []PimChangedRef `json:"changed"`
	Deleted   []string        `json:"deleted"` // deleted (or renamed-away) hrefs
	Truncated bool            `json:"truncated"`
}

// PimChangedRef is one changed resource in a sync diff.
type PimChangedRef struct {
	Href string `json:"href"`
	Etag string `json:"etag"`
	ID   string `json:"id"`
	UID  string `json:"uid"`
}

// PimPutResult is a PUT object response. Unparsed reports a body that was
// stored verbatim but could not be parsed — it round-trips byte-identically,
// but extracts, range queries, and scheduling cannot see inside it.
type PimPutResult struct {
	ID        string `json:"id"`
	Href      string `json:"href"`
	Etag      string `json:"etag"`
	Created   bool   `json:"created"`
	SyncToken string `json:"syncToken"`
	Unparsed  bool   `json:"unparsed,omitempty"`
}

// PimObjectDeleted is a DELETE object response.
type PimObjectDeleted struct {
	Deleted   bool   `json:"deleted"`
	ID        string `json:"id"`
	Href      string `json:"href"`
	UID       string `json:"uid"`
	SyncToken string `json:"syncToken"`
}

// PimMoveResult is a rename (href move) response.
type PimMoveResult struct {
	ID        string `json:"id"`
	Etag      string `json:"etag"`
	SyncToken string `json:"syncToken"`
}

// PimRespondResult is an RSVP outcome. Exactly one of OrganizerUpdated (the
// organizer's copy lives on this deployment and was patched) or ReplySent (a
// METHOD:REPLY was mailed to a remote organizer) is true — or neither, when the
// organizer could not be told.
type PimRespondResult struct {
	Partstat         string `json:"partstat"`
	Etag             string `json:"etag"`
	OrganizerUpdated bool   `json:"organizerUpdated"`
	ReplySent        bool   `json:"replySent"`
}

// PimCollectionDeleted is a DELETE collection response.
type PimCollectionDeleted struct {
	Deleted          bool  `json:"deleted"`
	ObjectsDestroyed int64 `json:"objectsDestroyed"`
}

// PimShare is one sharing grant on a collection.
type PimShare struct {
	ShareeMailboxID string `json:"shareeMailboxId"`
	// ShareeAddress is the sharee's primary address, or null for an
	// address-less identity (a calendar-only user, which the PIM surface
	// supports by design). Carried so a listing names a person rather than a
	// ULID; fall back to the id when absent.
	ShareeAddress *string `json:"shareeAddress"`
	Permission    string  `json:"permission"` // read | read-write
	CreatedAt     int64   `json:"createdAt"`
}

// PimSharedWithMe is one collection another mailbox shared with this one.
type PimSharedWithMe struct {
	OwnerMailboxID string `json:"ownerMailboxId"`
	// OwnerAddress is the owner's primary address, or null when they have none.
	// Carried so a listing names a person rather than a ULID.
	OwnerAddress *string `json:"ownerAddress"`
	CollectionID string  `json:"collectionId"`
	Permission   string  `json:"permission"`
	Kind         string  `json:"kind"`
	Name         string  `json:"name"`
	DisplayName  *string `json:"displayName"`
	Color        *string `json:"color"`
	Description  *string `json:"description"`
	SyncToken    string  `json:"syncToken"`
	CreatedAt    int64   `json:"createdAt"`
}

// PimPublicCollection is one entry of the account-scoped public directory. ID
// is the directory-entry handle subscribe takes (not the collection id).
type PimPublicCollection struct {
	ID             string  `json:"id"`
	OwnerMailboxID string  `json:"ownerMailboxId"`
	CollectionID   string  `json:"collectionId"`
	Kind           string  `json:"kind"`
	DisplayName    *string `json:"displayName"`
	Description    *string `json:"description"`
	Category       *string `json:"category"`
	CreatedAt      int64   `json:"createdAt"`
}

// PimToken is one feed token (hash-stored; the plaintext appears only at mint).
type PimToken struct {
	ID             string  `json:"id"`
	CollectionID   string  `json:"collectionId"`
	Kind           string  `json:"kind"`
	Label          *string `json:"label"`
	ExpiresAt      *int64  `json:"expiresAt"`
	AccessCount    int64   `json:"accessCount"`
	LastAccessedAt *int64  `json:"lastAccessedAt"`
	CreatedAt      int64   `json:"createdAt"`
}

// PimTokenCreated is the mint response: the token row plus the one-time
// plaintext and the ready-to-share unauthenticated feed URL.
type PimTokenCreated struct {
	PimToken
	Token string `json:"token"`
	URL   string `json:"url"`
}

// PimImportItem is one item of a bulk import outcome.
type PimImportItem struct {
	UID    string  `json:"uid"`
	Href   *string `json:"href"`   // where it landed; nil when failed
	Status string  `json:"status"` // created | replaced | failed
	Etag   string  `json:"etag,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// PimImportResult is the bulk import response (HTTP 200 all-ok, 207 partial —
// both decode here).
type PimImportResult struct {
	Items         []PimImportItem `json:"items"`
	CreatedCount  int64           `json:"createdCount"`
	ReplacedCount int64           `json:"replacedCount"`
	FailedCount   int64           `json:"failedCount"`
	// SyncToken is the collection's token after the last successful write;
	// empty if nothing landed.
	SyncToken string `json:"syncToken,omitempty"`
}

// PimCollectionCreateInput is the create-collection body. Name is the URL slug
// (letters, digits, and . _ ~ -).
type PimCollectionCreateInput struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"displayName,omitempty"`
	Color       string  `json:"color,omitempty"`
	Description string  `json:"description,omitempty"`
	Role        *string `json:"role,omitempty"`
	Visibility  string  `json:"visibility,omitempty"`
}

// PimObjectListOpts filters an objects listing. Start/End/Component/Expand
// apply to the calendars family only (the range query); Expand requires both
// bounds. Fields="meta" omits the raw content from each row.
type PimObjectListOpts struct {
	Limit     int
	Cursor    string
	Fields    string
	UID       string
	Start     *int64
	End       *int64
	Component string
	Expand    bool
}

// PimPutOpts carries the PUT preconditions. IfNoneMatchStar makes the PUT an
// exclusive create; IfMatch guards a replace with the stored ETag.
type PimPutOpts struct {
	IfMatch         string
	IfNoneMatchStar bool
}

func pimBase(s PimScope, kind PimKind) string {
	return "/mailboxes/" + escapeSegment(s.MailboxID) + "/" + string(kind)
}

func pimColPath(s PimScope, kind PimKind, ref string) string {
	return pimBase(s, kind) + "/" + escapeSegment(ref)
}

func pimObjPath(s PimScope, kind PimKind, ref, href string) string {
	return pimColPath(s, kind, ref) + "/objects/" + escapeSegment(href)
}

// pimMedia is the wire media type of a family's raw bodies.
func pimMedia(kind PimKind) string {
	if kind == PimAddressbooks {
		return "text/vcard"
	}
	return "text/calendar"
}

// ListPimCollections lists the mailbox's calendars or addressbooks.
func (c *Client) ListPimCollections(ctx context.Context, s PimScope, kind PimKind) ([]PimCollection, error) {
	var out struct {
		Calendars    []PimCollection `json:"calendars"`
		Addressbooks []PimCollection `json:"addressbooks"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimBase(s, kind), headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	if kind == PimAddressbooks {
		return out.Addressbooks, nil
	}
	return out.Calendars, nil
}

// CreatePimCollection creates a calendar or addressbook. 409 name_taken /
// too_many_collections.
func (c *Client) CreatePimCollection(ctx context.Context, s PimScope, kind PimKind, in PimCollectionCreateInput) (*PimCollection, error) {
	var out PimCollection
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: pimBase(s, kind), headers: s.headers(),
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPimCollection fetches one collection by ULID or name slug.
func (c *Client) GetPimCollection(ctx context.Context, s PimScope, kind PimKind, ref string) (*PimCollection, error) {
	var out PimCollection
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimColPath(s, kind, ref), headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePimCollection applies a partial update (rename, display properties,
// visibility). patch fields follow core's PATCH body; include
// "expectedVisibility" to CAS a visibility change (409 visibility_conflict on a
// lost race).
func (c *Client) UpdatePimCollection(ctx context.Context, s PimScope, kind PimKind, ref string, patch map[string]any) (*PimCollection, error) {
	var out PimCollection
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: pimColPath(s, kind, ref), headers: s.headers(),
		body: mustJSON(patch), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePimCollection deletes a collection and all its objects.
func (c *Client) DeletePimCollection(ctx context.Context, s PimScope, kind PimKind, ref string) (*PimCollectionDeleted, error) {
	var out PimCollectionDeleted
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: pimColPath(s, kind, ref), headers: s.headers(),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PimCollectionChanges returns the sync diff since an opaque token (omit since
// for the initial full listing). A 410 invalid_sync_token means the token
// expired or predates the collection — resync from scratch.
func (c *Client) PimCollectionChanges(ctx context.Context, s PimScope, kind PimKind, ref, since string, limit int) (*PimChanges, error) {
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	var out PimChanges
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimColPath(s, kind, ref) + "/changes",
		query: q, headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPimObjects lists a collection's objects, optionally range-filtered
// (calendars) and/or expanded into occurrences.
func (c *Client) ListPimObjects(ctx context.Context, s PimScope, kind PimKind, ref string, opts PimObjectListOpts) (*PimObjectPage, error) {
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.Fields != "" {
		q.Set("fields", opts.Fields)
	}
	if opts.UID != "" {
		q.Set("uid", opts.UID)
	}
	if opts.Start != nil {
		q.Set("start", itoa64(*opts.Start))
	}
	if opts.End != nil {
		q.Set("end", itoa64(*opts.End))
	}
	if opts.Component != "" {
		q.Set("component", opts.Component)
	}
	if opts.Expand {
		q.Set("expand", "true")
	}
	var out PimObjectPage
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimColPath(s, kind, ref) + "/objects",
		query: q, headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPimObject streams one object's raw iCalendar/vCard text. uid, when
// non-empty, looks the object up by UID instead of href (the href segment is
// still required by the route; pass any placeholder). The ReadCloser MUST be
// closed. Response headers carry ETag, X-Pim-Object-Id, X-Pim-Uid,
// X-Pim-Updated-At, X-Pim-Component.
func (c *Client) GetPimObject(ctx context.Context, s PimScope, kind PimKind, ref, href, uid string) (io.ReadCloser, http.Header, error) {
	q := url.Values{}
	if uid != "" {
		q.Set("uid", uid)
	}
	// The kernel's blanket Accept: application/json would negotiate core's JSON
	// representation of the object (Vary: Accept) — override it to get the wire
	// text this method promises.
	h := map[string]string{"Accept": pimMedia(kind)}
	for k, v := range s.headers() {
		h[k] = v
	}
	resp, err := c.do(ctx, request{
		method: http.MethodGet, path: pimObjPath(s, kind, ref, href),
		query: q, headers: h, idempotent: true, streamResult: true,
	})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp.Header, nil
}

// GetPimObjectJSON fetches one object as JSCalendar/JSContact instead of wire
// text — the same resource under content negotiation (core sets Vary: Accept).
func (c *Client) GetPimObjectJSON(ctx context.Context, s PimScope, kind PimKind, ref, href, uid string) (*PimObjectJSON, error) {
	q := url.Values{}
	if uid != "" {
		q.Set("uid", uid)
	}
	var out PimObjectJSON
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimObjPath(s, kind, ref, href),
		query: q, headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PutPimObjectJSON creates or replaces one object from a JSCalendar Event or
// JSContact Card. Core converts JSON→wire text before the ordinary write path
// runs, so ETag, If-Match, uid-conflict and iTIP scheduling all behave exactly
// as they do for a wire-text PUT.
func (c *Client) PutPimObjectJSON(ctx context.Context, s PimScope, kind PimKind, ref, href string, data map[string]any, opts PimPutOpts) (*PimPutResult, error) {
	h := map[string]string{}
	for k, v := range s.headers() {
		h[k] = v
	}
	if opts.IfMatch != "" {
		h["If-Match"] = opts.IfMatch
	}
	if opts.IfNoneMatchStar {
		h["If-None-Match"] = "*"
	}
	var out PimPutResult
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: pimObjPath(s, kind, ref, href),
		body: mustJSON(data), contentType: "application/json", headers: h, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PutPimObject creates or replaces one object from raw wire text. 412
// precondition_failed carries the stored ETag; 409 uid_conflict names the href
// already holding that UID.
func (c *Client) PutPimObject(ctx context.Context, s PimScope, kind PimKind, ref, href string, body []byte, opts PimPutOpts) (*PimPutResult, error) {
	h := map[string]string{}
	for k, v := range s.headers() {
		h[k] = v
	}
	if opts.IfMatch != "" {
		h["If-Match"] = opts.IfMatch
	}
	if opts.IfNoneMatchStar {
		h["If-None-Match"] = "*"
	}
	var out PimPutResult
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: pimObjPath(s, kind, ref, href),
		body: body, contentType: pimMedia(kind), headers: h, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePimObject deletes one object (by href, or by uid when set), optionally
// guarded by If-Match.
func (c *Client) DeletePimObject(ctx context.Context, s PimScope, kind PimKind, ref, href, uid, ifMatch string) (*PimObjectDeleted, error) {
	q := url.Values{}
	if uid != "" {
		q.Set("uid", uid)
	}
	h := map[string]string{}
	for k, v := range s.headers() {
		h[k] = v
	}
	if ifMatch != "" {
		h["If-Match"] = ifMatch
	}
	var out PimObjectDeleted
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: pimObjPath(s, kind, ref, href),
		query: q, headers: h,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// MovePimObject renames an object's href within its collection (a DAV MOVE).
func (c *Client) MovePimObject(ctx context.Context, s PimScope, kind PimKind, ref, href, to string) (*PimMoveResult, error) {
	var out PimMoveResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: pimObjPath(s, kind, ref, href) + "/move",
		body: mustJSON(map[string]string{"to": to}), contentType: "application/json",
		headers: s.headers(),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RespondPimObject records the caller's RSVP on a calendar object and tells the
// organizer (patching a local organizer's copy, or mailing a METHOD:REPLY to a
// remote one). 403 not_attendee when no owned address of the acting mailbox is
// among the attendees.
func (c *Client) RespondPimObject(ctx context.Context, s PimScope, ref, href, partstat string) (*PimRespondResult, error) {
	var out PimRespondResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: pimObjPath(s, PimCalendars, ref, href) + "/respond",
		body: mustJSON(map[string]string{"partstat": partstat}), contentType: "application/json",
		headers: s.headers(),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPimShares lists a collection's sharing grants (owner only).
func (c *Client) ListPimShares(ctx context.Context, s PimScope, kind PimKind, ref string) ([]PimShare, error) {
	var out struct {
		Shares []PimShare `json:"shares"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimColPath(s, kind, ref) + "/shares",
		headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Shares, nil
}

// PutPimShare grants (or re-grants with a new permission) a sharee's access.
// The grant alone does not open the collection — its visibility must also be
// shared or public.
func (c *Client) PutPimShare(ctx context.Context, s PimScope, kind PimKind, ref, shareeMailboxID, permission string) (*PimShare, error) {
	var out PimShare
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: pimColPath(s, kind, ref) + "/shares/" + escapeSegment(shareeMailboxID),
		body: mustJSON(map[string]string{"permission": permission}), contentType: "application/json",
		headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePimShare revokes a sharee's grant.
func (c *Client) DeletePimShare(ctx context.Context, s PimScope, kind PimKind, ref, shareeMailboxID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: pimColPath(s, kind, ref) + "/shares/" + escapeSegment(shareeMailboxID),
		headers: s.headers(),
	}, nil)
}

// ListPimTokens lists a collection's feed tokens (never the plaintext).
func (c *Client) ListPimTokens(ctx context.Context, s PimScope, kind PimKind, ref string) ([]PimToken, error) {
	var out struct {
		Tokens []PimToken `json:"tokens"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: pimColPath(s, kind, ref) + "/tokens",
		headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Tokens, nil
}

// CreatePimToken mints a feed token. The plaintext and feed URL are revealed
// exactly once in the response. expiresAt is unix seconds; 0 = never.
func (c *Client) CreatePimToken(ctx context.Context, s PimScope, kind PimKind, ref, label string, expiresAt int64) (*PimTokenCreated, error) {
	in := map[string]any{}
	if label != "" {
		in["label"] = label
	}
	if expiresAt > 0 {
		in["expiresAt"] = expiresAt
	}
	var out PimTokenCreated
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: pimColPath(s, kind, ref) + "/tokens",
		body: mustJSON(in), contentType: "application/json", headers: s.headers(),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokePimToken revokes a feed token.
func (c *Client) RevokePimToken(ctx context.Context, s PimScope, kind PimKind, ref, tokenID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: pimColPath(s, kind, ref) + "/tokens/" + escapeSegment(tokenID),
		headers: s.headers(),
	}, nil)
}

// ExportPimCollection streams the whole collection as one merged iCalendar /
// vCard document. The ReadCloser MUST be closed.
func (c *Client) ExportPimCollection(ctx context.Context, s PimScope, kind PimKind, ref string) (io.ReadCloser, http.Header, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet, path: pimColPath(s, kind, ref) + "/export",
		headers: s.headers(), idempotent: true, streamResult: true,
	})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp.Header, nil
}

// ImportPimCollection bulk-imports a merged iCalendar/vCard document. Hrefs
// derive from UIDs, so a re-run converges (200 all-ok, 207 partial failure —
// both decode; check Failed). Import performs no iTIP scheduling.
func (c *Client) ImportPimCollection(ctx context.Context, s PimScope, kind PimKind, ref string, body []byte) (*PimImportResult, error) {
	var out PimImportResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: pimColPath(s, kind, ref) + "/import",
		body: body, contentType: pimMedia(kind), headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPimSharedWithMe lists collections other mailboxes shared with this one.
func (c *Client) ListPimSharedWithMe(ctx context.Context, s PimScope) ([]PimSharedWithMe, error) {
	var out struct {
		Shared []PimSharedWithMe `json:"shared"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/mailboxes/" + escapeSegment(s.MailboxID) + "/pim/shared-with-me",
		headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Shared, nil
}

// ListPimPublicDirectory lists the account's public collections (excluding the
// acting mailbox's own listings).
func (c *Client) ListPimPublicDirectory(ctx context.Context, s PimScope) ([]PimPublicCollection, error) {
	var out struct {
		Public []PimPublicCollection `json:"public"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/mailboxes/" + escapeSegment(s.MailboxID) + "/pim/public-directory",
		headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Public, nil
}

// ListPimSubscriptions lists the mailbox's public-directory subscriptions.
func (c *Client) ListPimSubscriptions(ctx context.Context, s PimScope) ([]PimPublicCollection, error) {
	var out struct {
		Subscriptions []PimPublicCollection `json:"subscriptions"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/mailboxes/" + escapeSegment(s.MailboxID) + "/pim/subscriptions",
		headers: s.headers(), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Subscriptions, nil
}

// SubscribePimPublic subscribes to a public-directory entry (by its directory
// id), which also grants read access to the underlying collection.
func (c *Client) SubscribePimPublic(ctx context.Context, s PimScope, publicID string) (*PimPublicCollection, error) {
	var out PimPublicCollection
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(s.MailboxID) + "/pim/subscriptions/" + escapeSegment(publicID),
		headers: s.headers(),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UnsubscribePimPublic removes a subscription.
func (c *Client) UnsubscribePimPublic(ctx context.Context, s PimScope, publicID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/mailboxes/" + escapeSegment(s.MailboxID) + "/pim/subscriptions/" + escapeSegment(publicID),
		headers: s.headers(),
	}, nil)
}

// GetPimFeed streams a published feed by its token — the one bearer-exempt PIM
// path (the token IS the credential); works on an unauthenticated client. The
// ReadCloser MUST be closed.
func (c *Client) GetPimFeed(ctx context.Context, token string) (io.ReadCloser, http.Header, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet, path: "/pim/feeds/" + escapeSegment(token),
		idempotent: true, streamResult: true,
	})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp.Header, nil
}
