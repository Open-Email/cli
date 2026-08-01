package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// vacationFormPane edits one mailbox's out-of-office auto-reply. A single
// document, so a form rather than a listing.
//
// The form reads the CURRENT document at build time and writes back under its
// state token, so a change made elsewhere since the screen opened is refused
// rather than silently overwritten.
//
// The one semantic worth putting on screen: turning the reply ON starts a NEW
// absence and re-arms every correspondent for another reply, while editing the
// text of an already-enabled reply notifies nobody. The two are different
// actions, so the form's final step names which one is about to happen.
func vacationFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox) pane {
	return newLoaderPane(ctx, ui, "Auto-reply — "+mailboxLabel(&mbx),
		func(lctx context.Context, c *coreapi.Client) (pane, error) {
			cur, err := c.GetVacation(lctx, mbx.ID)
			if err != nil {
				return nil, err
			}
			return vacationEditPane(ctx, ui, mbx, cur), nil
		})
}

func vacationEditPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox, cur *coreapi.Vacation) pane {
	var (
		enabled = cur.IsEnabled
		subject = strOr(cur.Subject, "")
		body    = strOr(cur.TextBody, "")
		from    = strOr(cur.FromDate, "")
		to      = strOr(cur.ToDate, "")
	)
	wasEnabled := cur.IsEnabled
	return newFormPane(ctx, ui, formSpec{
		title: "Auto-reply — " + mailboxLabel(&mbx),
		build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Subject").Value(&subject).
					Description("what the reply's subject line says"),
				huh.NewText().Title("Reply").Lines(6).Value(&body).
					Description("plain text · sent once per correspondent per absence"),
				huh.NewInput().Title("From").Value(&from).
					Description("optional start, YYYY-MM-DD or an RFC3339 instant · blank for none"),
				huh.NewInput().Title("Until").Value(&to).
					Description("optional end · blank for none"),
				// A trailing Select whose options ALL act: a huh Confirm here
				// would read as a submit button and toggle only on ←/→, which is
				// how console toggles get submitted un-flipped.
				huh.NewSelect[bool]().Title("Save as").
					Description(vacationSaveHint(wasEnabled)).
					Options(
						huh.NewOption("on — replying while you are away", true),
						huh.NewOption("off — keep the text, stop replying", false),
					).
					Value(&enabled),
			))
		},
		submit: func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
			in := coreapi.VacationInput{IsEnabled: enabled}
			if s := strings.TrimSpace(subject); s != "" {
				in.Subject = &s
			}
			if b := body; strings.TrimSpace(b) != "" {
				in.TextBody = &b
			}
			// The HTML body is preserved untouched: this form does not edit it,
			// and dropping it because it is not on screen would be a silent
			// deletion of something another client wrote.
			in.HtmlBody = cur.HtmlBody
			for _, spec := range [...]struct {
				label string
				value string
				dst   **string
			}{{"From", from, &in.FromDate}, {"Until", to, &in.ToDate}} {
				v := strings.TrimSpace(spec.value)
				if v == "" {
					continue
				}
				when, err := parseVacationDateTUI(v)
				if err != nil {
					return "", nil, fmt.Errorf("%s: %w", spec.label, err)
				}
				*spec.dst = &when
			}
			if in.IsEnabled && in.TextBody == nil && in.HtmlBody == nil {
				return "", nil, fmt.Errorf("an enabled auto-reply needs a message")
			}
			// The state read at build time is the CAS guard.
			v, err := c.PutVacation(sctx, mbx.ID, in, cur.State)
			if err != nil {
				if ae, ok := coreapi.AsAPIError(err); ok {
					if ae.Status == 412 {
						return "", nil, fmt.Errorf("the auto-reply changed elsewhere while this was open — reopen and re-apply")
					}
					if ae.Code == "invalid_window" {
						return "", nil, fmt.Errorf("From must be before Until")
					}
				}
				return "", nil, err
			}
			switch {
			case v.IsEnabled && !wasEnabled:
				return "auto-reply ON — a new absence; everyone is eligible for one reply", nil, nil
			case v.IsEnabled:
				return "auto-reply updated — nobody was re-notified", nil, nil
			default:
				return "auto-reply OFF — the message is kept", nil, nil
			}
		},
	})
}

// vacationSaveHint names which of the two actions the current choice performs,
// since "on" from off is a new absence and "on" from on is a plain edit.
func vacationSaveHint(wasEnabled bool) string {
	if wasEnabled {
		return "already on — saving edits the message and re-notifies nobody"
	}
	return "currently off — turning it on starts a new absence"
}

// parseVacationDateTUI normalizes a typed date to the exact UTC instant core's
// schema requires (`2006-01-02T15:04:05Z`). Kept here rather than shared with
// the CLI's parser because `tui` cannot import `cli` — the dependency runs the
// other way.
func parseVacationDateTUI(s string) (string, error) {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	}
	return "", fmt.Errorf("unparseable date %q (try YYYY-MM-DD)", s)
}
