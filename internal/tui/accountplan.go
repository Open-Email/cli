package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/Open-Email/cli/internal/coreapi"
)

// The account PLAN screen: every per-account governance knob in one form.
//
// Core deliberately has no plan concept — only these independent columns — so
// "is this a paid account?" is answered by seven separate values that an
// operator otherwise has to remember to set one at a time, from two different
// commands, with no single place that shows the resulting tier. Half-provisioned
// accounts are the predictable outcome: vanity hostnames granted while the send
// caps stay at the platform default, or a storage pool raised on an account
// still frozen. Showing them together is the point of this pane.
//
// Every field here is SYSTEM-ONLY at core (`PATCH /accounts/:accountId` refuses
// a non-system principal on the handler's first line, before it reads the body),
// so this pane cannot grant a tenant anything they could have granted
// themselves — it is an operator convenience over a gate that lives elsewhere.

// accountPlan is the form's raw text state. Strings rather than typed values
// because every numeric field here has a THIRD state that no number expresses
// ("platform default"), and huh edits text.
type accountPlan struct {
	name string
	// sending mirrors core's own `sendHold` (nil | "paused" | "disabled") as
	// one select — the API is one field precisely so there is no pair to
	// reconcile, and the form keeps it that way.
	sending     string // enabled | paused | stopped
	vanityHosts bool
	maxMailboxes,
	msgsPerDay,
	rcptsPerDay,
	storage string
}

const (
	sendingEnabled = "enabled"
	sendingPaused  = "paused"
	sendingStopped = "stopped"
)

// planFromAccount seeds the form from the account as it stands, so an operator
// edits a value rather than re-types a tier from memory.
func planFromAccount(a coreapi.Account) accountPlan {
	p := accountPlan{
		name:         a.Name,
		sending:      sendingEnabled,
		vanityHosts:  a.VanityHosts,
		maxMailboxes: int64Or(a.MaxMailboxes, ""),
		msgsPerDay:   planCapText(a.SendMsgsPerDay),
		rcptsPerDay:  planCapText(a.SendRcptsPerDay),
		storage:      planBytesText(a.StorageLimitBytes),
	}
	switch sendHoldOf(a.SendHold) {
	case "disabled":
		p.sending = sendingStopped
	case "paused":
		p.sending = sendingPaused
	}
	return p
}

// planCapText renders a cap's three states in the SAME vocabulary the form
// accepts back, so the value an operator sees is one they could have typed.
// Empty means "platform default" — deliberately blank rather than the word, so
// the common case of leaving a field alone looks like leaving it alone.
func planCapText(p *int64) string {
	switch {
	case p == nil:
		return ""
	case *p == 0:
		return "unlimited"
	default:
		return strconv.FormatInt(*p, 10)
	}
}

func planBytesText(p *int64) string {
	switch {
	case p == nil:
		return ""
	case *p == 0:
		return "unlimited"
	default:
		return fmtBytes(*p)
	}
}

// parsePlanCap reads the three-state cap vocabulary the CLI's --send-*-per-day
// flags already use (`internal/cli/mailboxes.go`), so the two surfaces cannot
// disagree about what "default" means. nil = platform default; 0 = unlimited.
func parsePlanCap(field, s string) (*int64, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "default":
		return nil, nil
	case "unlimited", "none":
		zero := int64(0)
		return &zero, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("%s: expected a non-negative number, 'unlimited', or blank for the platform default", field)
	}
	return &n, nil
}

// parsePlanBytes is parsePlanCap for the storage pool, which additionally takes
// a human size — nobody should be asked to type 10737418240.
func parsePlanBytes(field, s string) (*int64, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "default":
		return nil, nil
	case "unlimited", "none":
		zero := int64(0)
		return &zero, nil
	}
	n, err := parseBytes(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return &n, nil
}

// parsePlanMailboxCap is the TWO-state one, and the difference is worth its own
// function: a nil maxMailboxes means UNLIMITED, not "platform default", so
// reusing the cap parser here would silently turn "unlimited" into a bound
// (or the reverse) on the one knob where that mistake creates mailboxes.
func parsePlanMailboxCap(s string) (*int64, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "unlimited", "none":
		return nil, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("max mailboxes: expected a non-negative number, or blank for unlimited")
	}
	return &n, nil
}

func int64PtrEq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// accountPlanPatch is the whole decision this pane makes, kept pure so it can be
// tested without a terminal.
//
// It emits ONLY changed fields. Sending the whole form would be simpler, but
// `PATCH /accounts/:accountId` is last-write-wins on every column it receives,
// so a full-form submit silently reverts anything another operator changed
// while this form sat open. Diffing narrows that window to the fields actually
// touched, which mirrors what the CLI already does with `flags.Changed`.
//
// An empty patch is returned as an empty map rather than an error: core answers
// `400 empty_patch`, and "you changed nothing" is a better thing to say locally
// than to make a round trip to be told off.
func accountPlanPatch(cur coreapi.Account, p accountPlan) (map[string]any, error) {
	patch := map[string]any{}

	name := strings.TrimSpace(p.name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if name != cur.Name {
		patch["name"] = name
	}

	// One field on the wire: "" (enabled) marshals as JSON null, which is how
	// core clears a hold.
	if want := planSendHold(p.sending); want != sendHoldOf(cur.SendHold) {
		if want == "" {
			patch["sendHold"] = nil
		} else {
			patch["sendHold"] = want
		}
	}

	if p.vanityHosts != cur.VanityHosts {
		patch["vanityHosts"] = p.vanityHosts
	}

	maxMbx, err := parsePlanMailboxCap(p.maxMailboxes)
	if err != nil {
		return nil, err
	}
	if !int64PtrEq(maxMbx, cur.MaxMailboxes) {
		patch["maxMailboxes"] = maxMbx
	}

	msgs, err := parsePlanCap("messages/day", p.msgsPerDay)
	if err != nil {
		return nil, err
	}
	if !int64PtrEq(msgs, cur.SendMsgsPerDay) {
		patch["sendMsgsPerDay"] = msgs
	}

	rcpts, err := parsePlanCap("recipients/day", p.rcptsPerDay)
	if err != nil {
		return nil, err
	}
	if !int64PtrEq(rcpts, cur.SendRcptsPerDay) {
		patch["sendRcptsPerDay"] = rcpts
	}

	storage, err := parsePlanBytes("storage pool", p.storage)
	if err != nil {
		return nil, err
	}
	if !int64PtrEq(storage, cur.StorageLimitBytes) {
		patch["storageLimitBytes"] = storage
	}

	return patch, nil
}

// planChangeSummary names what actually moved, so the flash reports the edit
// rather than the fact that a request succeeded. Sorted by how much an operator
// would want to see it first: the stops, then the grants, then the numbers.
func planChangeSummary(patch map[string]any) string {
	order := []struct {
		key, label string
	}{
		{"sendHold", "sending"},
		{"vanityHosts", "vanity hostnames"},
		{"maxMailboxes", "max mailboxes"},
		{"sendMsgsPerDay", "messages/day"},
		{"sendRcptsPerDay", "recipients/day"},
		{"storageLimitBytes", "storage pool"},
		{"name", "name"},
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(patch))
	for _, f := range order {
		if _, ok := patch[f.key]; ok && !seen[f.label] {
			seen[f.label] = true
			out = append(out, f.label)
		}
	}
	return strings.Join(out, ", ")
}

// accountPlanFormPane edits one account's whole plan.
func accountPlanFormPane(ctx context.Context, ui *Options, cur coreapi.Account) pane {
	p := planFromAccount(cur)
	return newFormPane(ctx, ui, formSpec{
		title: "Plan — " + cur.Name,
		build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Name").Value(&p.name).Validate(required("name")),
				huh.NewSelect[string]().Title("Sending").Value(&p.sending).Options(
					huh.NewOption("enabled", sendingEnabled),
					// The two stops are spelled by what happens to mail already
					// in the queue, because that is the difference an operator
					// is actually choosing between.
					huh.NewOption("paused — queued mail HELD, senders retry (reversible)", sendingPaused),
					huh.NewOption("stopped — queued mail BOUNCED, senders told no (abuse)", sendingStopped),
				),
				huh.NewConfirm().Title("Vanity hostnames").Value(&p.vanityHosts).
					Affirmative("allowed").Negative("no").
					Description("may this account claim its own mail./smtp./webmail./dav. names — the paid-plan gate; turning it off never revokes hostnames already serving clients"),
				huh.NewInput().Title("Max mailboxes").Value(&p.maxMailboxes).
					Description("blank = unlimited"),
				huh.NewInput().Title("Messages/day").Value(&p.msgsPerDay).
					Description("blank = platform default · 'unlimited' · or a number (account-wide, across every mailbox)"),
				huh.NewInput().Title("Recipients/day").Value(&p.rcptsPerDay).
					Description("blank = platform default · 'unlimited' · or a number"),
				huh.NewInput().Title("Storage pool").Value(&p.storage).
					Description("blank = platform default · 'unlimited' (metered) · or a size like 50G"),
			))
		},
		submit: func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
			patch, err := accountPlanPatch(cur, p)
			if err != nil {
				return "", nil, err
			}
			if len(patch) == 0 {
				return "nothing changed", nil, nil
			}
			if _, err := c.UpdateAccount(sctx, cur.ID, patch); err != nil {
				return "", nil, err
			}
			return "updated " + planChangeSummary(patch) + " on " + cur.Name, nil, nil
		},
	})
}

// sendHoldOf dereferences a resource's `sendHold` (nil = none).
func sendHoldOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// planSendHold maps the form's three-way select back to the wire value.
func planSendHold(sending string) string {
	switch sending {
	case sendingStopped:
		return "disabled"
	case sendingPaused:
		return "paused"
	}
	return ""
}
