package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// DkimKey is one generation of the platform signing key. There is ONE shared
// key signing all outbound mail (d= stays the sender's domain, so DMARC still
// aligns); the two selectors alternate across rotations, which is what lets a
// customer publish their CNAMEs once and never touch them again.
//
// PublishedAt is null while the TXT upsert is still pending or retrying, and
// ActivatedAt is null for a key that has never signed anything.
type DkimKey struct {
	Selector    string `json:"selector"` // oe1 | oe2
	State       string `json:"state"`    // staged | active | retired
	RecordName  string `json:"recordName"`
	PublicTxt   string `json:"publicTxt"`
	CreatedAt   int64  `json:"createdAt"`
	PublishedAt *int64 `json:"publishedAt"`
	ActivatedAt *int64 `json:"activatedAt"`
}

// DkimCname is one record the customer publishes on their own domain. Host is
// shown in relative form against <domain>.
type DkimCname struct {
	Host   string `json:"host"`
	Target string `json:"target"`
}

// DkimStatus is the whole rotation picture. Configured is the AND of zone id,
// DNS root, DNS token and KEK — false means the feature is inert, not broken,
// and mail relays unsigned rather than deferring.
type DkimStatus struct {
	Configured     bool    `json:"configured"`
	DNSRoot        *string `json:"dnsRoot"`
	ActiveSelector *string `json:"activeSelector"`
	// NextRunAt is the next rotation-alarm firing in ms epoch (note: ms, unlike
	// the epoch-SECONDS timestamps everywhere else in this API); null = not armed.
	NextRunAt *int64      `json:"nextRunAt"`
	Keys      []DkimKey   `json:"keys"`
	Cnames    []DkimCname `json:"cnames"`
}

// DkimRotated is the answer to both rotate and activate. Bootstrapped is true
// only when the call created the very first key, which is active immediately —
// there is no soak to wait out when nothing was signing before.
type DkimRotated struct {
	Bootstrapped   bool    `json:"bootstrapped"`
	ActiveSelector *string `json:"activeSelector"`
	StagedSelector *string `json:"stagedSelector"`
}

// GetDkim reports platform DKIM status (system-only).
func (c *Client) GetDkim(ctx context.Context) (*DkimStatus, error) {
	var out DkimStatus
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/dkim", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateDkim starts a rotation now (system-only): the next keypair is generated
// on the inactive selector and its TXT published; signing flips automatically
// after the 7-day soak. On a fresh deployment this bootstraps the first key.
//
// 409 rotation_in_progress when a staged key is already soaking — a rotate that
// races the alarm loses cleanly rather than double-staging. 503
// dkim_not_configured when the zone/root/token/KEK config is incomplete.
func (c *Client) RotateDkim(ctx context.Context) (*DkimRotated, error) {
	var out DkimRotated
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/dkim/rotate",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ActivateDkim flips the staged key to active now, skipping the rest of the
// soak (system-only). It refuses while the staged TXT is not resolver-visible
// (409 dkim_dns_not_ready); force skips that check, which signs with a key
// receivers may not be able to fetch — every message that lands before the TXT
// propagates fails DMARC. Use it only when the DoH view is known-stale.
func (c *Client) ActivateDkim(ctx context.Context, force bool) (*DkimRotated, error) {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	var out DkimRotated
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/dkim/activate", query: q,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
