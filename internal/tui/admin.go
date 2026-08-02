package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// suppressionsDesc lists the deployment-global do-not-send list. Rows are
// normally written by the feedback-loop consumer from a receiver's own hard
// bounce or spam complaint; the console's only verb is lift. A row CAN be
// entered by hand for a complaint that consumer could not attribute — that is
// `openemail admin suppressions add`, deliberately CLI-only, because it is a
// deployment-wide write no report backs and it should not be one keystroke away
// while browsing a list.
func suppressionsDesc() resourceDesc {
	return resourceDesc{
		key:     "suppressions",
		name:    "Suppressions",
		caption: "deployment-global do-not-send list · normally written from bounce and complaint reports",
		columns: []column{
			{title: "ADDRESS", flex: true},
			{title: "REASON", width: 12},
			{title: "EVENTS", width: 6},
			{title: "FIRST", width: 16},
			{title: "LAST", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListSuppressions(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, s := range pg.Items {
				rows[i] = rowData{
					cells: []string{
						s.Address, s.Reason, fmt.Sprintf("%d", s.EventCount),
						fmtEpoch(s.FirstEventAt), fmtEpoch(s.LastEventAt),
					},
					item: s,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			s := item.(coreapi.Suppression)
			return []kv{
				{k: "address", v: s.Address},
				{k: "reason", v: s.Reason},
				{k: "events", v: fmt.Sprintf("%d", s.EventCount)},
				{k: "first seen", v: fmtEpoch(s.FirstEventAt)},
				{k: "last seen", v: fmtEpoch(s.LastEventAt)},
				{k: "source delivery", v: strOr(s.SourceDeliveryID, "—")},
				{},
				{v: "Report detail"},
				{v: strOr(s.Detail, "(none recorded)")},
			}
		},
		actions: []action{
			{key: "u", label: "u lift", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return suppressionLiftConfirm(ctx, ui, item.(coreapi.Suppression))
			}},
		},
	}
}

func suppressionLiftConfirm(ctx context.Context, ui *Options, s coreapi.Suppression) pane {
	body := "Removes the suppression so mail to this address is attempted again.\n\n"
	switch s.Reason {
	case "complaint":
		body += "This address REPORTED mail from us as spam. Lifting it means mailing someone " +
			"who asked to stop, which is what damages a sending reputation in the first place."
	case "hard_bounce":
		body += "This address hard-bounced: the receiver said it does not exist. Unless the " +
			"mailbox has actually been created since, it will simply bounce and re-suppress."
	default:
		body += "Lift only when the underlying delivery problem is actually fixed."
	}
	if s.Detail != nil && *s.Detail != "" {
		body += "\n\nReported: " + *s.Detail
	}
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Lift suppression on " + s.Address,
		body:  body,
		verb:  "lift suppression",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if _, err := c.LiftSuppression(sctx, s.Address); err != nil {
				return "", err
			}
			return s.Address + " is clear to send", nil
		},
	})
}

// dkimDesc shows the platform signing keys. It is a one-object screen rendered
// as a listing of key generations, because the rows ARE the question: which
// selector signs today, and is the next one soaking yet.
func dkimDesc() resourceDesc {
	st := &dkimScreenState{}
	return resourceDesc{
		key:  "dkim",
		name: "DKIM",
		summary: func(w int) []string {
			return st.summaryLines(w)
		},
		columns: []column{
			{title: "SELECTOR", width: 10},
			{title: "STATE", width: 8},
			{title: "TXT RECORD", flex: true},
			{title: "CREATED", width: 16},
			{title: "PUBLISHED", width: 16},
			{title: "ACTIVATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			status, err := c.GetDkim(ctx)
			if err != nil {
				return nil, "", err
			}
			st.set(status)
			rows := make([]rowData, len(status.Keys))
			for i, k := range status.Keys {
				rows[i] = rowData{
					cells: []string{
						k.Selector, k.State, k.RecordName,
						fmtEpoch(k.CreatedAt), fmtEpochPtr(k.PublishedAt), fmtEpochPtr(k.ActivatedAt),
					},
					item: k,
				}
			}
			return rows, "", nil // two selectors; never paginated
		},
		detail: func(item any) []kv {
			k := item.(coreapi.DkimKey)
			kvs := []kv{
				{k: "selector", v: k.Selector},
				{k: "state", v: k.State},
				{k: "TXT record", v: k.RecordName},
				{k: "created", v: fmtEpoch(k.CreatedAt)},
				{k: "published", v: fmtEpochPtr(k.PublishedAt)},
				{k: "activated", v: fmtEpochPtr(k.ActivatedAt)},
				{},
				{v: "Published value"},
			}
			for _, line := range chunk(k.PublicTxt, 72) {
				kvs = append(kvs, kv{v: line})
			}
			return kvs
		},
		actions: []action{
			{key: "n", label: "n rotate", run: func(ctx context.Context, ui *Options, _ any) pane {
				return dkimRotateConfirm(ctx, ui)
			}},
			{key: "A", label: "A activate", run: func(ctx context.Context, ui *Options, _ any) pane {
				return dkimActivateConfirm(ctx, ui)
			}},
		},
	}
}

// dkimScreenState is written by the fetch goroutine and read by the render loop
// — see sieveScreenState for why that needs a mutex.
type dkimScreenState struct {
	mu     sync.Mutex
	loaded bool
	status *coreapi.DkimStatus
}

func (s *dkimScreenState) set(st *coreapi.DkimStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.status = true, st
}

// summaryLines states what is signing right now — the only fact that changes
// whether outbound mail passes DMARC.
func (s *dkimScreenState) summaryLines(w int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || s.status == nil {
		return nil
	}
	st := s.status
	var lines []string
	if !st.Configured {
		lines = append(lines, stErr.Render(truncate(
			"NOT CONFIGURED — outbound mail relays unsigned (needs DKIM_ZONE_ID, DKIM_DNS_ROOT, CF_DNS_API_TOKEN, DKIM_KEK)", w)))
	}
	if st.ActiveSelector == nil {
		lines = append(lines, stErr.Render(truncate("no active key — nothing is signing", w)))
	} else {
		line := fmt.Sprintf("signing with %s", *st.ActiveSelector)
		if st.NextRunAt != nil {
			line += " · next rotation check " + time.UnixMilli(*st.NextRunAt).Local().Format("2006-01-02 15:04")
		} else {
			line += " · rotation alarm not armed"
		}
		lines = append(lines, stLive.Render(truncate(line, w)))
	}
	for _, k := range st.Keys {
		if k.State == "staged" && k.PublishedAt == nil {
			lines = append(lines, stErr.Render(truncate(fmt.Sprintf(
				"%s is staged but its TXT is not confirmed published — the soak has not started", k.Selector), w)))
		}
	}
	return lines
}

func dkimRotateConfirm(ctx context.Context, ui *Options) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Start a DKIM rotation",
		body: "Generates the next keypair on the inactive selector and publishes its TXT. " +
			"Signing does NOT change today: the current key keeps signing through a 7-day " +
			"soak, then the flip happens on its own.\n\n" +
			"On a deployment with no keys at all this bootstraps the first one, which becomes " +
			"active immediately. If the scheduler already staged a key, this is refused " +
			"(rotation_in_progress) rather than staging a second.",
		verb: "start rotation",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			res, err := c.RotateDkim(sctx)
			if err != nil {
				return "", err
			}
			if res.Bootstrapped {
				return "bootstrapped — " + strOr(res.ActiveSelector, "the first key") + " is signing now", nil
			}
			return strOr(res.StagedSelector, "the next key") + " staged — the flip is automatic after the soak", nil
		},
	})
}

func dkimActivateConfirm(ctx context.Context, ui *Options) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Activate the staged key now",
		body: "Makes the staged key the signing key immediately, skipping the rest of the soak.\n\n" +
			"The soak exists because resolvers cache: core refuses (dkim_dns_not_ready) while " +
			"the staged TXT is not visible, since signing with a key receivers cannot fetch " +
			"fails DKIM and DMARC with it. Forcing past that check is CLI-only — " +
			"`openemail admin dkim activate --force`.",
		verb: "activate staged key",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			res, err := c.ActivateDkim(sctx, false)
			if err != nil {
				return "", err
			}
			return "activated — " + strOr(res.ActiveSelector, "the staged key") + " is signing", nil
		},
	})
}

// chunk splits a long single-line value (a public key) into readable rows.
func chunk(s string, n int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{"—"}
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
