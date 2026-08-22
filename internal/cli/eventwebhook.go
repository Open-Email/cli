package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// Event webhooks (docs/events-design.md §XIV): one URL per MAILBOX and one
// per DOMAIN receiving signed, fact-only event batches. The same four verbs
// under both groups — `mailboxes webhook …` (mailbox-scoped, -m) and `domains
// webhook … <domain>` — from one implementation, parameterised by scope.
//
// The secret is write-only and THREE-WAY on the wire: omit keeps, string
// rotates, null clears. `--secret` and `--clear-secret` spell the two non-
// default arms; both at once is refused. Nothing here ever prints a secret —
// `set` warns to stderr that the copy the caller holds is the only one (the
// `keys create` idiom without the stdout token, since there is nothing to
// show).

type eventWebhookScope string

const (
	eventWebhookMailbox eventWebhookScope = "mailbox"
	eventWebhookDomain  eventWebhookScope = "domain"
)

// eventWebhookTarget resolves the scope's identifier: the mailbox from -m /
// the default mailbox, or the positional domain.
func (s eventWebhookScope) target(ctx context.Context, a *app, client *coreapi.Client, args []string) (string, error) {
	if s == eventWebhookDomain {
		return args[0], nil
	}
	return a.resolveMailbox(ctx, client, "")
}

func (s eventWebhookScope) args() cobra.PositionalArgs {
	if s == eventWebhookDomain {
		return cobra.ExactArgs(1)
	}
	return cobra.NoArgs
}

func (s eventWebhookScope) use(verb string) string {
	if s == eventWebhookDomain {
		return verb + " <domain>"
	}
	return verb
}

func (s eventWebhookScope) get(ctx context.Context, c *coreapi.Client, id string) (*coreapi.EventWebhook, error) {
	if s == eventWebhookDomain {
		return c.GetDomainEventWebhook(ctx, id)
	}
	return c.GetMailboxEventWebhook(ctx, id)
}

func (s eventWebhookScope) put(ctx context.Context, c *coreapi.Client, id string, in coreapi.EventWebhookInput) (*coreapi.EventWebhook, error) {
	if s == eventWebhookDomain {
		return c.PutDomainEventWebhook(ctx, id, in)
	}
	return c.PutMailboxEventWebhook(ctx, id, in)
}

func (s eventWebhookScope) del(ctx context.Context, c *coreapi.Client, id string) error {
	if s == eventWebhookDomain {
		return c.DeleteDomainEventWebhook(ctx, id)
	}
	return c.DeleteMailboxEventWebhook(ctx, id)
}

func (s eventWebhookScope) test(ctx context.Context, c *coreapi.Client, id string) (*coreapi.EventWebhookTestResult, error) {
	if s == eventWebhookDomain {
		return c.TestDomainEventWebhook(ctx, id)
	}
	return c.TestMailboxEventWebhook(ctx, id)
}

// newEventWebhookCmd is the `webhook` group for one scope.
func newEventWebhookCmd(a *app, scope eventWebhookScope) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhook",
		Aliases: []string{"event-webhook", "events"},
		Short:   "The event webhook: signed, fact-only event batches POSTed to your endpoint",
		Long: "One URL per " + string(scope) + " receiving every event as ordered, signed, fact-only\n" +
			"batches — ids, states, an actor and a sequence, never content; your receiver\n" +
			"re-fetches through the API. Verify `X-OpenEmail-Signature` (HMAC-SHA-256 over\n" +
			"the RAW body bytes with your secret) before parsing, check `sentAt` against\n" +
			"your tolerance, dedupe on `id`, order by `sequence`, and reconcile via\n" +
			"`messages/changes` on any gap. Every non-2xx retries for ~3 days; a hook failing\n" +
			"for 24h is auto-disabled and any `set` re-enables it.\n\n" +
			"See `openemail watch` for the WebSocket reference client of the same vocabulary.",
	}
	if scope == eventWebhookMailbox {
		addMailboxFlag(cmd, a)
	}
	cmd.AddCommand(
		newEventWebhookGetCmd(a, scope),
		newEventWebhookSetCmd(a, scope),
		newEventWebhookDeleteCmd(a, scope),
		newEventWebhookTestCmd(a, scope),
	)
	return cmd
}

func printEventWebhook(a *app, hook *coreapi.EventWebhook) {
	a.out.Emit(hook, func(w io.Writer) {
		status := "enabled"
		if !hook.Enabled {
			status = "disabled"
			if hook.DisabledReason != nil {
				status += " — " + *hook.DisabledReason
				if hook.FailingSince != nil {
					status += " since " + fmtEpoch(*hook.FailingSince)
				}
			}
		}
		secret := "none"
		if hook.HasSecret {
			secret = "set — write-only"
		}
		scopeID := hook.Domain
		if hook.Scope == "mailbox" {
			scopeID = hook.MailboxID
		}
		printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
			{"Scope", hook.Scope + " " + scopeID},
			{"Endpoint", hook.URL},
			{"Signing secret", secret},
			{"Status", status},
			{"Last delivered", fmtEpochPtr(hook.LastDeliveredAt)},
			{"Last failure", fmtEpochPtr(hook.LastFailureAt)},
			{"Consecutive failures", fmt.Sprint(hook.ConsecutiveFailures)},
		})
		if hook.LastFailure != nil && *hook.LastFailure != "" {
			a.out.Msgf("")
			a.out.Msgf("last failure: %s", *hook.LastFailure)
		}
		if !hook.Enabled {
			a.out.Msgf("")
			a.out.Msgf("re-enable with `webhook set --url %s` (the stored secret is kept)", hook.URL)
		}
	})
}

func newEventWebhookGetCmd(a *app, scope eventWebhookScope) *cobra.Command {
	return &cobra.Command{
		Use:     scope.use("get"),
		Aliases: []string{"show", "status"},
		Short:   "Show the event webhook and its delivery status",
		Args:    scope.args(),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, args)
			if err != nil {
				return err
			}
			hook, err := scope.get(cmd.Context(), client, id)
			if err != nil {
				return err
			}
			printEventWebhook(a, hook)
			return nil
		},
	}
}

func newEventWebhookSetCmd(a *app, scope eventWebhookScope) *cobra.Command {
	var (
		url         string
		secret      string
		clearSecret bool
	)
	cmd := &cobra.Command{
		Use:     scope.use("set"),
		Aliases: []string{"put", "create", "update"},
		Short:   "Create or replace the event webhook (any set re-enables a disabled hook)",
		Long: "Sets the endpoint URL (HTTPS, on a public host). The signing secret is\n" +
			"write-only: omit --secret to keep the stored one, pass --secret to rotate it,\n" +
			"pass --clear-secret to remove it. Keep your copy — it is never shown again.",
		Args: scope.args(),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated before authentication: contradictory flags are a usage
			// error whatever credentials the caller holds.
			if cmd.Flags().Changed("secret") && clearSecret {
				return usageError(errors.New("--secret and --clear-secret are mutually exclusive"))
			}
			if url == "" {
				return usageError(errors.New("--url is required (a hook is its URL; there is no \"keep\" for it)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, args)
			if err != nil {
				return err
			}
			in := coreapi.EventWebhookInput{URL: url, ClearSecret: clearSecret}
			if cmd.Flags().Changed("secret") {
				s := secret
				in.Secret = &s
			}
			hook, err := scope.put(cmd.Context(), client, id, in)
			if err != nil {
				return err
			}
			if in.Secret != nil {
				fmt.Fprintln(os.Stderr, "Signing secret stored write-only — keep your copy; it is never shown again.")
			}
			printEventWebhook(a, hook)
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "the receiver's URL (HTTPS, public host)")
	cmd.Flags().StringVar(&secret, "secret", "", "HMAC signing secret to set or rotate (omit to keep the stored one)")
	cmd.Flags().BoolVar(&clearSecret, "clear-secret", false, "remove the stored signing secret")
	return cmd
}

func newEventWebhookDeleteCmd(a *app, scope eventWebhookScope) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     scope.use("delete"),
		Aliases: []string{"remove", "rm"},
		Short:   "Remove the event webhook",
		Args:    scope.args(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm("Remove the event webhook? Batches already queued for it are acked without delivery.") {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, args)
			if err != nil {
				return err
			}
			if err := scope.del(cmd.Context(), client, id); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true}, func(io.Writer) {
				a.out.Successf("Event webhook removed")
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newEventWebhookTestCmd(a *app, scope eventWebhookScope) *cobra.Command {
	return &cobra.Command{
		Use:   scope.use("test"),
		Short: "Queue a webhook.test batch through the real delivery path",
		Long: "Queues one `webhook.test` event for this scope's hook. The outcome shows on\n" +
			"`webhook get` (last delivered / last failure) and in the traffic log as\n" +
			"source `events` — the batch id printed here is its delivery id.",
		Args: scope.args(),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, args)
			if err != nil {
				return err
			}
			res, err := scope.test(cmd.Context(), client, id)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(io.Writer) {
				a.out.Successf("Test event queued: batch %s", res.BatchID)
				a.out.Msgf("watch `webhook get` for last delivered / last failure, or the traffic log under source=events")
			})
			return nil
		},
	}
}
