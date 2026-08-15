package cli

import (
	"github.com/spf13/cobra"
)

func newUpgradeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Explain how to upgrade (openemail does not self-update)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.out.Msgf("openemail %s\n", Version)
			a.out.Msgf("openemail is installed by a package manager, which owns the binary — there is")
			a.out.Msgf("no self-update. Upgrade with whichever you used:")
			a.out.Msgf("  Homebrew:  brew upgrade openemail")
			a.out.Msgf("  Scoop:     scoop update openemail")
			a.out.Msgf("  apt/deb:   apt-get update && apt-get install --only-upgrade openemail")
			a.out.Msgf("  rpm/dnf:   dnf upgrade openemail")
			a.out.Msgf("  go:        go install github.com/Open-Email/cli/cmd/openemail@latest")
			a.out.Msgf("  manual:    download from https://github.com/Open-Email/cli/releases")
			return nil
		},
	}
}
