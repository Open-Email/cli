package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newUpgradeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Explain how to upgrade (openemail does not self-update)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(os.Stderr, "openemail %s\n\n", Version)
			fmt.Fprintln(os.Stderr, "openemail is installed by a package manager, which owns the binary — there is")
			fmt.Fprintln(os.Stderr, "no self-update. Upgrade with whichever you used:")
			fmt.Fprintln(os.Stderr, "  Homebrew:  brew upgrade openemail")
			fmt.Fprintln(os.Stderr, "  Scoop:     scoop update openemail")
			fmt.Fprintln(os.Stderr, "  apt/deb:   apt-get update && apt-get install --only-upgrade openemail")
			fmt.Fprintln(os.Stderr, "  rpm/dnf:   dnf upgrade openemail")
			fmt.Fprintln(os.Stderr, "  go:        go install github.com/openemail/openemail-cli/cmd/openemail@latest")
			fmt.Fprintln(os.Stderr, "  manual:    download from https://github.com/openemail/openemail-cli/releases")
			return nil
		},
	}
}
