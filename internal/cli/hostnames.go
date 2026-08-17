package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Open-Email/cli/internal/coreapi"
)

// hostnameServices is the fixed set core defines. Listed here so `set` can
// refuse a typo locally with a useful message instead of a 404 from a path
// segment the server never matched.
var hostnameServices = []string{"mail", "smtp", "webmail", "dav"}

// newDomainHostnamesCmd is the vanity-hostname surface: a customer's own
// `mail.`/`smtp.`/`webmail.`/`dav.` names in front of the platform services.
//
// The shape mirrors what the feature actually is — a CLAIM, then a DNS change
// the customer makes, then a CHECK that decides whether anything is served. So
// `set` never reports success at serving; only `check` (or a later `list`) can.
func newDomainHostnamesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hostnames",
		Short: "Vanity hostnames — the domain's own names for mail, smtp, webmail and dav",
		Long: "Put a customer's OWN hostnames in front of the platform services: they publish one " +
			"CNAME per service and their users configure `mail.acme.com` rather than a platform " +
			"hostname.\n\n" +
			"Claiming is not serving. A claim records that the hostname belongs to this domain (it " +
			"must be a strict subdomain of it); nothing is served until `hostnames check` sees the " +
			"CNAME actually pointing at the platform target. A hostname whose CNAME is later removed " +
			"stops being served on the next check.",
	}
	cmd.AddCommand(
		newHostnamesListCmd(a),
		newHostnamesSetCmd(a),
		newHostnamesDeleteCmd(a),
		newHostnamesCheckCmd(a),
	)
	return cmd
}

func newHostnamesListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list <domain>",
		Short: "Show every service slot, its hostname and the last check's verdict",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.ListDomainHostnames(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) { printHostnames(w, a.out, res) })
			return nil
		},
	}
}

func newHostnamesSetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "set <domain> <service> <hostname>",
		Short: "Claim a hostname for one service (mail | smtp | webmail | dav)",
		Long: "Claim a hostname for one service. It must be a STRICT subdomain of the domain — which is " +
			"what makes ownership self-evident, since the domain's own control was already proved.\n\n" +
			"The claim is recorded immediately; publishing the CNAME is the customer's next step, and " +
			"`hostnames check` is what turns a published CNAME into a served hostname.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain, service, hostname := args[0], strings.ToLower(args[1]), args[2]
			if !isHostnameService(service) {
				return fmt.Errorf("unknown service %q (want one of: %s)", service, strings.Join(hostnameServices, ", "))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.SetDomainHostname(cmd.Context(), domain, service, hostname)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				printHostnames(w, a.out, res)
				// Say what is left to do, in the customer's terms. A claim that
				// reports nothing further looks finished and is not.
				if slot := slotFor(res, service); slot != nil && slot.Target != nil {
					a.out.Msgf("Publish: %s CNAME %s", hostname, *slot.Target)
					a.out.Msgf("%s", a.out.Dim("Then run `openemail domains hostnames check "+domain+"`."))
				}
			})
			return nil
		},
	}
}

func newHostnamesDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <domain> <service>",
		Short: "Release a service's vanity hostname",
		Long: "Release the hostname claimed for one service. For webmail and dav this also removes the " +
			"Cloudflare custom hostname, and the command FAILS if that removal fails — leaving a " +
			"hostname serving a domain we no longer host would be worse than a retryable error.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service := strings.ToLower(args[1])
			if !isHostnameService(service) {
				return fmt.Errorf("unknown service %q (want one of: %s)", service, strings.Join(hostnameServices, ", "))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if err := client.DeleteDomainHostname(cmd.Context(), args[0], service); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true}, func(w io.Writer) {
				a.out.Msgf("Released the %s hostname for %s.", service, args[0])
			})
			return nil
		},
	}
}

func newHostnamesCheckCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "check <domain>",
		Short: "Re-check every claimed hostname and store the verdict",
		Long: "Re-resolve every claimed hostname and store the result. This is the only thing that makes " +
			"a hostname servable — and the only thing that un-makes it: a hostname whose CNAME no " +
			"longer points at the platform stops being served and stops being renewed.\n\n" +
			"Liveness is a resolver view, so it can lag your DNS provider by a few minutes. A verdict " +
			"is cached briefly; --force re-queries now.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.CheckDomainHostnames(cmd.Context(), args[0], force)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) { printHostnames(w, a.out, res) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "bypass the cached verdict and re-check now")
	return cmd
}

func isHostnameService(s string) bool {
	for _, known := range hostnameServices {
		if s == known {
			return true
		}
	}
	return false
}

func slotFor(list *coreapi.DomainHostnameList, service string) *coreapi.DomainHostname {
	for i := range list.Hostnames {
		if list.Hostnames[i].Service == service {
			return &list.Hostnames[i]
		}
	}
	return nil
}

func printHostnames(w io.Writer, p *Printer, list *coreapi.DomainHostnameList) {
	rows := make([][]string, 0, len(list.Hostnames))
	for _, h := range list.Hostnames {
		hostname := "—"
		if h.Hostname != nil {
			hostname = *h.Hostname
		}
		target := "not offered"
		if h.Target != nil {
			target = *h.Target
		}
		rows = append(rows, []string{h.Service, hostname, target, hostnameState(h)})
	}
	printTable(w, p, []string{"SERVICE", "HOSTNAME", "CNAME TARGET", "STATE"}, rows)
}

// hostnameState collapses the row into the one thing an operator wants to read
// off a list: what is this hostname doing right now, and if not working, why.
//
// VerifiedAt is the authority — it is what the edges enumerate — and everything
// after it is the reason a verified hostname might still not be answering yet.
func hostnameState(h coreapi.DomainHostname) string {
	if h.Hostname == nil {
		return "unclaimed"
	}
	if h.VerifiedAt == nil {
		if h.Status == nil {
			return "claimed, not checked"
		}
		switch h.Status.CNAME {
		case "missing":
			return "CNAME not published"
		case "mismatch":
			return "CNAME points elsewhere"
		case "unknown":
			return "DNS unavailable"
		}
		if h.Status.CAA == "blocked" {
			return "blocked by CAA"
		}
		return "not verified"
	}
	if h.Status != nil {
		if h.Status.CF != nil {
			if !h.Status.CF.Configured {
				return "verified (not provisioned)"
			}
			if !h.Status.CF.Ready {
				return "verified, certificate pending"
			}
			return "live"
		}
		switch h.Status.Certificate {
		case "live":
			return "live"
		case "pending":
			// Sampled from one node behind round-robin DNS, so this is expected
			// for a few minutes after verification and never on its own a fault.
			return "verified, certificate pending"
		}
	}
	return "verified"
}
