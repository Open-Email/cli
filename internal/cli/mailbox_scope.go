package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// addMailboxFlag registers the persistent -m/--mailbox flag on a mailbox-scoped
// group command. All mailbox-scoped groups (messages, labels, threads, search,
// sieve, pickups) share the one app-level field; only one group runs per
// invocation so a single field is safe.
func addMailboxFlag(cmd *cobra.Command, a *app) {
	cmd.PersistentFlags().StringVarP(&a.flagMailbox, "mailbox", "m", "",
		"mailbox id or address (defaults to the profile's default mailbox)")
}

// addListOrderFlags registers the listing-order pair on a list command. They are
// two independent axes and are registered together so no listing can grow one
// without the other: --order is the DIRECTION, --sort-by is WHICH DATE.
//
// Both default to empty, meaning "let core choose" (desc, arrival) rather than
// this client restating a default core owns.
func addListOrderFlags(cmd *cobra.Command, order, sortBy *string) {
	cmd.Flags().StringVar(order, "order", "", "listing direction: desc (default, newest first) or asc")
	cmd.Flags().StringVar(sortBy, "sort-by", "",
		"sort key: arrival (default, when mail reached the mailbox) or date (the Date header, falling back to arrival)")
}

// validateListOrder rejects a bad enum locally instead of spending a round trip
// on core's validation_failed. The message names the accepted values, which the
// server's envelope does not.
//
// It does NOT try to validate a cursor against the key: core answers
// invalid_cursor for a cursor minted under the other sortBy, and that refusal is
// the authority — this client cannot tell the two shapes apart without parsing
// a token it is contractually meant to treat as opaque.
func validateListOrder(order, sortBy string) error {
	switch order {
	case "", "asc", "desc":
	default:
		return usageError(fmt.Errorf("--order must be asc or desc, got %q", order))
	}
	switch sortBy {
	case "", "arrival", "date":
	default:
		return usageError(fmt.Errorf("--sort-by must be arrival or date, got %q", sortBy))
	}
	return nil
}

// resolveMailbox resolves the effective mailbox id for a mailbox-scoped command.
//
// Precedence: explicit --mailbox flag > profile.DefaultMailbox > error (exit 2).
// A value containing '@' is treated as an address and resolved via
// GET /routes/:address — the route must be a mailbox destination. A value without
// '@' is used verbatim as a ULID (trusted, like every other id the CLI accepts).
// Resolutions are cached for the command's lifetime.
func (a *app) resolveMailbox(ctx context.Context, client *coreapi.Client, flagValue string) (string, error) {
	val := firstNonEmpty(flagValue, a.flagMailbox, a.profile.DefaultMailbox)
	if val == "" {
		return "", usageError(errors.New(
			"no mailbox — pass --mailbox <id|address> or set a default with `openemail mailboxes use <id>`"))
	}
	if a.mailboxCache != nil {
		if id, ok := a.mailboxCache[val]; ok {
			return id, nil
		}
	}

	id := val
	if strings.Contains(val, "@") {
		if a.profile.Role == coreapi.PrincipalMailbox {
			return "", usageError(fmt.Errorf(
				"cannot resolve address %q as a mailbox principal — pass the mailbox id (ULID) instead", val))
		}
		route, err := client.GetRoute(ctx, val)
		if err != nil {
			return "", err
		}
		if route.DestinationType != "mailbox" || route.MailboxID == nil || *route.MailboxID == "" {
			return "", usageError(fmt.Errorf(
				"address %q routes to %s, not a mailbox — pass a mailbox address or id", val, route.DestinationType))
		}
		id = *route.MailboxID
	}

	if a.mailboxCache == nil {
		a.mailboxCache = map[string]string{}
	}
	a.mailboxCache[val] = id
	return id, nil
}

// saveProfile persists the (possibly mutated) active profile to the config file.
func (a *app) saveProfile() error {
	a.cfg.SetProfile(a.profileName, a.profile)
	return a.cfg.Save()
}
