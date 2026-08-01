package cli

import (
	"errors"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newAdminDkimCmd exposes platform DKIM key rotation. Rotation is automatic —
// a 30-day cadence with a 7-day soak, driven by an alarm — so these commands
// exist to observe it and, rarely, to push it along.
func newAdminDkimCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dkim",
		Short: "Platform DKIM key status and rotation (system-only)",
		Long: "One shared RSA key signs all outbound mail, with d= set to each sender's own\n" +
			"domain so DMARC still aligns. Customers CNAME two selectors at the platform\n" +
			"zone once and never touch DNS again: rotation happens on our side by\n" +
			"alternating which selector is active.\n\n" +
			"The cycle is automatic — generate and publish on the inactive selector every\n" +
			"30 days, soak for 7 so resolvers pick the record up, then flip. These commands\n" +
			"observe that cycle; `rotate` and `activate` only make it happen sooner.",
	}
	cmd.AddCommand(
		newDkimStatusCmd(a),
		newDkimRotateCmd(a),
		newDkimActivateCmd(a),
	)
	return cmd
}

func newDkimStatusCmd(a *app) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"get", "show"},
		Short:   "Show key generations, the active selector, and the customer CNAME targets",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			st, err := client.GetDkim(cmd.Context())
			if err != nil {
				return err
			}
			a.out.Emit(st, func(w io.Writer) {
				if !st.Configured {
					// Inert, not broken: mail still relays, just unsigned.
					a.out.Warnf("DKIM is not configured — outbound mail relays UNSIGNED")
					a.out.Msgf("  needs DKIM_ZONE_ID, DKIM_DNS_ROOT, CF_DNS_API_TOKEN and DKIM_KEK")
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Configured", boolYN(st.Configured)},
					{"DNS root", strOr(st.DNSRoot, "—")},
					{"Active selector", strOr(st.ActiveSelector, "none — nothing is signing")},
					{"Next rotation check", fmtEpochMillisPtr(st.NextRunAt)},
				})
				if len(st.Keys) > 0 {
					a.out.Msgf("")
					rows := make([][]string, 0, len(st.Keys))
					for _, k := range st.Keys {
						rows = append(rows, []string{
							k.Selector, k.State, k.RecordName,
							fmtEpoch(k.CreatedAt), fmtEpochPtr(k.PublishedAt), fmtEpochPtr(k.ActivatedAt),
						})
					}
					printTable(w, a.out, []string{"SELECTOR", "STATE", "TXT RECORD", "CREATED", "PUBLISHED", "ACTIVATED"}, rows)
					// A staged key with no publishedAt is the one state worth
					// calling out: the soak clock has not started.
					for _, k := range st.Keys {
						if k.State == "staged" && k.PublishedAt == nil {
							a.out.Warnf("selector %s is staged but its TXT is not confirmed published — the soak has not started", k.Selector)
						}
					}
				}
				if len(st.Cnames) > 0 {
					a.out.Msgf("")
					a.out.Msgf("Customer records (published once per sending domain, then never again):")
					rows := make([][]string, 0, len(st.Cnames))
					for _, c := range st.Cnames {
						host := c.Host
						if domain != "" {
							host = renderCnameHost(c.Host, domain)
						}
						rows = append(rows, []string{host, "CNAME", c.Target})
					}
					printTable(w, a.out, []string{"HOST", "TYPE", "TARGET"}, rows)
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "render the customer CNAME hosts against this domain (paste-ready)")
	return cmd
}

func newDkimRotateCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Start a rotation now (or bootstrap the first key)",
		Long: "Generates the next keypair on the inactive selector and publishes its TXT.\n" +
			"Signing keeps using the current key and flips automatically after the 7-day\n" +
			"soak — this does not change what is signing today.\n\n" +
			"On a fresh deployment with no keys at all, this bootstraps the first one,\n" +
			"which becomes active immediately (there is no soak to wait out when nothing\n" +
			"was signing before).\n\n" +
			"Refused with rotation_in_progress if a staged key is already soaking; that\n" +
			"is the scheduler having got there first, not an error to work around.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm("Start a DKIM key rotation now?") {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			res, err := client.RotateDkim(cmd.Context())
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				if res.Bootstrapped {
					a.out.Successf("Bootstrapped the first DKIM key — selector %s is signing now", strOr(res.ActiveSelector, "?"))
					a.out.Msgf("  each sending domain still needs its two CNAMEs — see `openemail admin dkim status --domain <domain>`")
					return
				}
				a.out.Successf("Rotation started — selector %s is staged", strOr(res.StagedSelector, "?"))
				a.out.Msgf("  %s keeps signing through the 7-day soak; the flip is automatic", strOr(res.ActiveSelector, "the active key"))
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newDkimActivateCmd(a *app) *cobra.Command {
	var force, yes bool
	cmd := &cobra.Command{
		Use:   "activate",
		Short: "Flip the staged key to active now, skipping the rest of the soak",
		Long: "Makes the staged key the signing key immediately.\n\n" +
			"The soak exists because resolvers cache: a key that is published but not yet\n" +
			"visible everywhere will sign mail that receivers cannot verify. Core therefore\n" +
			"refuses (dkim_dns_not_ready) until the staged TXT resolves.\n\n" +
			"--force skips that check. Every message sent before the record propagates\n" +
			"fails DKIM and, with it, DMARC — use it only when you know the DoH view is\n" +
			"stale rather than the record missing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			prompt := "Activate the staged DKIM key now?"
			if force {
				prompt = "Activate the staged key WITHOUT the resolver-visibility check? Mail may fail DMARC until DNS propagates."
			}
			if !yes && !confirm(prompt) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			res, err := client.ActivateDkim(cmd.Context(), force)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Activated — selector %s is signing", strOr(res.ActiveSelector, "?"))
				if res.StagedSelector != nil {
					a.out.Msgf("  staged: %s", *res.StagedSelector)
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the resolver-visibility check (signs with a key receivers may not fetch)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// renderCnameHost fills a concrete domain into the host template core returns,
// so an operator can paste the record straight into a customer's zone. Core
// spells the host with a literal "<domain>" placeholder (oe1._domainkey.<domain>)
// — appending the domain instead of substituting it would produce a name that
// looks plausible and resolves nowhere.
func renderCnameHost(host, domain string) string {
	if host == "" {
		return host
	}
	if strings.Contains(host, "<domain>") {
		return strings.ReplaceAll(host, "<domain>", domain)
	}
	return host + "." + domain
}

// fmtEpochMillisPtr renders the DKIM alarm's next firing. It is the one
// timestamp in this API expressed in MILLIseconds, so it cannot share
// fmtEpochPtr without silently reading as a date in 1970.
func fmtEpochMillisPtr(ms *int64) string {
	if ms == nil {
		return "not armed"
	}
	return time.UnixMilli(*ms).Local().Format("2006-01-02 15:04")
}
