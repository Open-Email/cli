package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// pickupsDesc lists one mailbox's POP3 pickup sources — remote accounts core
// polls and delivers into this mailbox. The column that matters is STATUS:
// a source that keeps failing is auto-disabled, and until someone looks, mail
// simply stops arriving with nothing else to notice.
func pickupsDesc(mbx coreapi.Mailbox) resourceDesc {
	st := &pickupsScreenState{}
	return resourceDesc{
		key:  "pickups:" + mbx.ID,
		name: "Pickups — " + mailboxLabel(&mbx),
		summary: func(w int) []string {
			return st.summaryLines(w)
		},
		columns: []column{
			{title: "NAME", flex: true},
			{title: "HOST", flex: true},
			{title: "USERNAME", width: 20},
			{title: "EVERY", width: 7},
			{title: "ON", width: 3},
			{title: "STATUS", width: 12},
			{title: "LAST RUN", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListPickups(ctx, mbx.ID, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			st.set(pg.Items)
			rows := make([]rowData, len(pg.Items))
			for i, p := range pg.Items {
				rows[i] = rowData{
					cells: []string{
						strOr(p.Name, "—"), p.Host, p.Username,
						fmt.Sprintf("%dm", p.IntervalMinutes), yn(p.Enabled),
						pickupStatus(p), fmtEpochPtr(p.LastRunAt),
					},
					item: p,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			p := item.(coreapi.PickupSource)
			return []kv{
				{k: "id", v: p.ID},
				{k: "name", v: strOr(p.Name, "—")},
				{k: "host", v: fmt.Sprintf("%s:%d (%s)", p.Host, p.Port, p.TLS)},
				{k: "username", v: p.Username},
				{k: "interval", v: fmt.Sprintf("every %d minutes", p.IntervalMinutes)},
				// The destructive one: with it on, a fetched message is gone from
				// the remote server and this mailbox is the only copy.
				{k: "delete after fetch", v: yn(p.DeleteAfterFetch)},
				{k: "enabled", v: yn(p.Enabled)},
				{},
				{v: "Last run"},
				{k: "when", v: fmtEpochPtr(p.LastRunAt)},
				{k: "status", v: pickupStatus(p)},
				{k: "error", v: strOr(p.LastError, "—")},
				{k: "consecutive failures", v: fmt.Sprintf("%d", p.ConsecutiveFailures)},
				{k: "created", v: fmtEpoch(p.CreatedAt)},
			}
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return pickupFormPane(ctx, ui, mbx, nil)
			}},
			{key: "e", label: "e edit", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				p := item.(coreapi.PickupSource)
				return pickupFormPane(ctx, ui, mbx, &p)
			}},
			// NOT `g`: screenPane matches actions BEFORE the table sees the key,
			// and `g` is the table's goto-top. Someone scrolling a long list and
			// pressing `g` to jump back would instead fire an immediate,
			// unconfirmed fetch — which on a deleteAfterFetch source DELETES those
			// messages from the remote server.
			{key: "R", label: "R run now", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				p := item.(coreapi.PickupSource)
				status, err := c.RunPickup(ctx, mbx.ID, p.ID)
				if err != nil {
					return "", err
				}
				// The run is scheduled, not finished — say so, or an empty
				// mailbox a second later reads as a failure.
				return "run " + status + " — results appear once the fetch completes", nil
			}},
			{key: "t", label: "t on/off", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				p := item.(coreapi.PickupSource)
				on := !p.Enabled
				if _, err := c.UpdatePickup(ctx, mbx.ID, p.ID, map[string]any{"enabled": on}); err != nil {
					return "", err
				}
				if on {
					return "source enabled — polling resumes", nil
				}
				return "source disabled — polling stopped", nil
			}},
			{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return pickupDeleteConfirm(ctx, ui, mbx, item.(coreapi.PickupSource))
			}},
		},
	}
}

// pickupAutoDisableThreshold mirrors core's PICKUP_AUTO_DISABLE_THRESHOLD
// (src/do/pickup.ts): the failure count at which core switches a source off by
// itself.
const pickupAutoDisableThreshold = 5

// pickupStatus renders the run outcome plus the failure streak.
//
// Nothing in the API distinguishes an operator disable from an auto-disable, so
// "disabled (auto)" is only claimed once the streak has actually reached core's
// threshold. Below it, an operator who switched a flapping source off while they
// fixed the remote account would otherwise be told the platform did it.
func pickupStatus(p coreapi.PickupSource) string {
	s := strOr(p.LastStatus, "never run")
	if p.ConsecutiveFailures > 0 {
		s = fmt.Sprintf("%s ×%d", s, p.ConsecutiveFailures)
	}
	if !p.Enabled {
		if p.ConsecutiveFailures >= pickupAutoDisableThreshold {
			return "disabled (auto)"
		}
		return "disabled"
	}
	return s
}

// pickupsScreenState is written by the fetch goroutine and read by the render
// loop — see sieveScreenState for why that needs a mutex rather than a bare
// assignment.
type pickupsScreenState struct {
	mu      sync.Mutex
	loaded  bool
	sources []coreapi.PickupSource
}

func (s *pickupsScreenState) set(sources []coreapi.PickupSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.sources = true, sources
}

// summaryLines surfaces failing sources, because a broken pickup is silent:
// mail stops arriving and nothing else says why.
func (s *pickupsScreenState) summaryLines(w int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || len(s.sources) == 0 {
		return nil
	}
	var failing, autoOff int
	for _, p := range s.sources {
		if p.ConsecutiveFailures > 0 {
			failing++
			if !p.Enabled && p.ConsecutiveFailures >= pickupAutoDisableThreshold {
				autoOff++
			}
		}
	}
	if failing == 0 {
		return nil
	}
	msg := fmt.Sprintf("%d source(s) failing", failing)
	if autoOff > 0 {
		msg += fmt.Sprintf(", %d auto-disabled — no mail is being fetched from them", autoOff)
	}
	return []string{stErr.Render(truncate(msg, w))}
}

func pickupDeleteConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, p coreapi.PickupSource) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete pickup " + strOr(p.Name, p.Host),
		body: "Removes the source and its stored password. Mail already fetched into this " +
			"mailbox stays. To pause polling without losing the configuration, press t to " +
			"switch it off instead.",
		verb: "delete pickup",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.DeletePickup(sctx, mbx.ID, p.ID); err != nil {
				return "", err
			}
			return "pickup source deleted", nil
		},
	})
}

// pickupFormPane creates or edits a source. On edit the password field is left
// blank and only sent when filled — core never returns it, so an empty box
// must mean "keep", never "clear".
func pickupFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox, cur *coreapi.PickupSource) pane {
	var (
		name     string
		host     string
		port     = "995"
		username string
		password string
		tls      = "tls"
		interval = "15"
		delAfter = true
		enabled  = true
	)
	editing := cur != nil
	if editing {
		name = strOr(cur.Name, "")
		host, username = cur.Host, cur.Username
		port = strconv.FormatInt(cur.Port, 10)
		tls = cur.TLS
		interval = strconv.FormatInt(cur.IntervalMinutes, 10)
		delAfter, enabled = cur.DeleteAfterFetch, cur.Enabled
	}
	title := "New pickup source — " + mailboxLabel(&mbx)
	if editing {
		title = "Edit pickup " + strOr(cur.Name, cur.Host)
	}
	return newFormPane(ctx, ui, formSpec{
		title: title,
		build: func() *huh.Form {
			pwDesc := "POP3 password (stored encrypted; never returned)"
			if editing {
				pwDesc = "leave blank to keep the stored password"
			}
			return huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Name").Value(&name).Description("display label (optional)"),
				huh.NewInput().Title("Host").Value(&host),
				huh.NewInput().Title("Port").Value(&port),
				huh.NewSelect[string]().Title("Transport").Value(&tls).
					Options(huh.NewOption("TLS (implicit, port 995)", "tls"), huh.NewOption("STARTTLS", "starttls")),
				huh.NewInput().Title("Username").Value(&username),
				huh.NewInput().Title("Password").Value(&password).EchoMode(huh.EchoModePassword).Description(pwDesc),
				huh.NewInput().Title("Interval (minutes)").Value(&interval),
				huh.NewConfirm().Title("Delete from the server after fetching").Value(&delAfter).
					Affirmative("yes").Negative("no"),
				huh.NewConfirm().Title("Enabled").Value(&enabled).Affirmative("yes").Negative("no"),
			))
		},
		submit: func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
			portN, err := strconv.ParseInt(strings.TrimSpace(port), 10, 64)
			if err != nil {
				return "", nil, fmt.Errorf("port must be a number")
			}
			ivN, err := strconv.ParseInt(strings.TrimSpace(interval), 10, 64)
			if err != nil {
				return "", nil, fmt.Errorf("interval must be a number of minutes")
			}
			if editing {
				patch := map[string]any{
					"host": strings.TrimSpace(host), "port": portN, "tls": tls,
					"username": strings.TrimSpace(username), "intervalMinutes": ivN,
					"deleteAfterFetch": delAfter, "enabled": enabled,
				}
				// An explicit null clears the display name; an empty string is
				// not the same thing to core.
				if n := strings.TrimSpace(name); n != "" {
					patch["name"] = n
				} else {
					patch["name"] = nil
				}
				if password != "" {
					patch["password"] = password
				}
				if _, err := c.UpdatePickup(sctx, mbx.ID, cur.ID, patch); err != nil {
					return "", nil, err
				}
				return "pickup source updated", nil, nil
			}
			if password == "" {
				return "", nil, fmt.Errorf("a password is required to create a source")
			}
			in := coreapi.PickupCreateInput{
				Host: strings.TrimSpace(host), Port: portN, TLS: tls,
				Username: strings.TrimSpace(username), Password: password,
				IntervalMinutes: &ivN, DeleteAfterFetch: &delAfter,
			}
			if n := strings.TrimSpace(name); n != "" {
				in.Name = &n
			}
			created, err := c.CreatePickup(sctx, mbx.ID, in)
			if err != nil {
				return "", nil, err
			}
			// A source created disabled would silently never poll, so say which
			// state it landed in rather than a bare "created".
			if !enabled {
				if _, uerr := c.UpdatePickup(sctx, mbx.ID, created.ID, map[string]any{"enabled": false}); uerr != nil {
					return "", nil, uerr
				}
				return "pickup source created (disabled — press t to start polling)", nil, nil
			}
			return "pickup source created — first poll within " + interval + " minutes", nil, nil
		},
	})
}
