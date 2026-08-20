package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	a := &app{}
	root := newRootCmd(a)
	err := root.Execute()
	if err == nil {
		a.maybeNotifyUpdate()
		return 0
	}

	code := 1
	var ee *ExitError
	if errors.As(err, &ee) {
		code = ee.Code
	} else if coreapi.IsUnauthorized(err) {
		code = 4
	}
	a.printError(err)
	a.maybeNotifyUpdate()
	return code
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "openemail",
		Short: "Command-line client for the OpenEmail platform",
		Long: "openemail is the command-line client for the OpenEmail platform.\n\n" +
			"Authenticate once with `openemail login`; the CLI mints its own account\n" +
			"API key, stores it in your OS keychain, and uses it for every command.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Resolve config/profile/token before every command.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.preRun()
		},
	}
	root.SetVersionTemplate("openemail {{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringVar(&a.flagProfile, "profile", "", "config profile to use (env: OPENEMAIL_PROFILE)")
	pf.StringVar(&a.flagAPIURL, "api-url", "", "override the API base URL (env: OPENEMAIL_API_URL)")
	pf.StringVar(&a.flagAPIKey, "api-key", "", "use this bearer token for one invocation (env: OPENEMAIL_API_KEY)")
	pf.BoolVar(&a.flagJSON, "json", false, "output machine-readable JSON")
	pf.BoolVar(&a.flagNoColor, "no-color", false, "disable color output")
	pf.BoolVar(&a.flagDebug, "debug", false, "print debug detail on errors")
	pf.BoolVar(&a.flagNoKeyring, "no-keyring", false, "store keys in a 0600 file instead of the OS keychain (env: OPENEMAIL_NO_KEYRING)")

	// Dynamic completion for --profile from the config's profile names.
	_ = root.RegisterFlagCompletionFunc("profile", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		cfg, err := config.Load()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, 0, len(cfg.Profiles))
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	})

	root.AddCommand(
		newLoginCmd(a),
		newLogoutCmd(a),
		newWhoamiCmd(a),
		newStatusCmd(a),
		newUICmd(a),
		newKeysCmd(a),
		newMailboxesCmd(a),
		newAccountsCmd(a),
		newDomainsCmd(a),
		newRoutesCmd(a),
		newPatternsCmd(a),
		newCredentialsCmd(a),
		newIdentitiesCmd(a),
		newMessagesCmd(a),
		newLabelsCmd(a),
		newThreadsCmd(a),
		newSearchCmd(a),
		newRulesCmd(a),
		newPrefsCmd(a),
		newSieveCmd(a),
		newVacationCmd(a),
		newCalendarsCmd(a),
		newAddressbooksCmd(a),
		newPimCmd(a),
		newPickupsCmd(a),
		newComposeCmd(a),
		newSendCmd(a),
		newSuppressionsCmd(a),
		newListsCmd(a),
		newWatchCmd(a),
		newDeliverCmd(a),
		newAPICmd(a),
		newAdminCmd(a),
		newUpgradeCmd(a),
		newVersionCmd(a),
	)
	// Decide admin-group help visibility now (see eagerProfileRole): --help skips
	// PersistentPreRunE, so this cannot wait until preRun.
	if a.adminCmd != nil {
		a.adminCmd.Hidden = eagerProfileRole() != coreapi.PrincipalSystem
	}
	return root
}

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.out.Emit(map[string]string{"version": Version}, func(w io.Writer) {
				fmt.Fprintf(w, "openemail %s\n", Version)
			})
			return nil
		},
	}
}

// errorHint returns a one-line remedy for the error codes whose bare name
// doesn't tell a user what to do next. Empty when the code speaks for itself.
func errorHint(ae *coreapi.APIError) string {
	addr, _ := ae.Extra["address"].(string)
	switch ae.Code {
	case "address_taken":
		// Create-and-bind: the address is already routed to some mailbox.
		if addr != "" {
			return fmt.Sprintf("%s is already routed — choose another address, or delete its route to reassign it", addr)
		}
		return "that address is already routed — choose another, or delete its route to reassign it"
	case "address_not_routed":
		// The primary-address label must name a route to THIS mailbox first.
		if addr != "" {
			return fmt.Sprintf("bind it first: openemail routes create %s --type mailbox --mailbox <id>", addr)
		}
		return "bind the address to this mailbox first (openemail routes create … --type mailbox --mailbox <id>)"
	case "account_required":
		// System keys must choose domain ownership explicitly (core refuses to
		// mint a tenant-invisible platform domain by omission).
		return "system keys must choose ownership: pass --account <id>, or --platform for a domain owned by no account"
	case "sending_not_writable":
		// Sending is earned from DNS, not set. Point at the loop that grants it.
		return "sending is activated by DNS, not by a flag: publish the SPF and both DKIM records (openemail domains dns <domain>), then re-run openemail domains create <domain>"
	case "domain_claimed_elsewhere":
		// The caller proved control, so core told them the truth rather than
		// hiding behind the anti-enumeration 409. There is no self-service move.
		return "you control this domain's DNS, but another account holds it here — contact support to have it transferred"
	case "verification_unavailable":
		// A resolver outage, not the customer's DNS. Retrying is the whole fix.
		return "DNS could not be queried just now — nothing was changed; try again shortly"
	}
	return ""
}

func (a *app) printError(err error) {
	if errors.Is(err, errSilent) {
		return // the command already rendered its report; only the exit code matters
	}
	p := a.out
	if p == nil {
		p = newPrinter(a.flagJSON, a.flagNoColor)
	}
	if ae, ok := coreapi.AsAPIError(err); ok {
		fmt.Fprintf(os.Stderr, "%s %s\n", p.paint(colRed, "error:"), ae.Error())
		if ae.Line > 0 {
			fmt.Fprintf(os.Stderr, "  at line %d, column %d\n", ae.Line, ae.Col)
		}
		if d := ae.Detail(); d != "" {
			fmt.Fprintln(os.Stderr, d)
		}
		// Core answers 404 for both missing and cross-tenant resources (no
		// existence leaks), so be honest about the ambiguity.
		if ae.Status == 404 {
			fmt.Fprintln(os.Stderr, p.Dim("  (the resource does not exist, or is not accessible with this key)"))
		}
		if h := errorHint(ae); h != "" {
			fmt.Fprintln(os.Stderr, p.Dim("  "+h))
		}
		if a.flagDebug && len(ae.Body) > 0 {
			fmt.Fprintf(os.Stderr, "%s\n", p.Dim(string(ae.Body)))
		}
		return
	}
	fmt.Fprintf(os.Stderr, "%s %v\n", p.paint(colRed, "error:"), err)
}
