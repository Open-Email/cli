package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newUpgradeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "upgrade",
		Short:   "Explain how to upgrade (openemail does not self-update)",
		Long:    "Print the package-manager commands to upgrade openemail.",
		Example: "  openemail upgrade\n  openemail upgrade --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			methods := map[string]string{
				"homebrew": "brew upgrade openemail",
				"scoop":    "scoop update openemail",
				"apt":      "apt-get update && apt-get install --only-upgrade openemail",
				"dnf":      "dnf upgrade openemail",
				"go":       "go install github.com/Open-Email/cli/cmd/openemail@latest",
				"manual":   "https://github.com/Open-Email/cli/releases",
			}
			a.out.Emit(map[string]any{"version": Version, "methods": methods}, func(w io.Writer) {
				fmt.Fprintf(w, "openemail %s\n\n", Version)
				fmt.Fprintln(w, "openemail is installed by a package manager, which owns the binary — there is")
				fmt.Fprintln(w, "no self-update. Upgrade with whichever you used:")
				fmt.Fprintln(w, "  Homebrew:  brew upgrade openemail")
				fmt.Fprintln(w, "  Scoop:     scoop update openemail")
				fmt.Fprintln(w, "  apt/deb:   apt-get update && apt-get install --only-upgrade openemail")
				fmt.Fprintln(w, "  rpm/dnf:   dnf upgrade openemail")
				fmt.Fprintln(w, "  go:        go install github.com/Open-Email/cli/cmd/openemail@latest")
				fmt.Fprintln(w, "  manual:    download from https://github.com/Open-Email/cli/releases")
			})
			return nil
		},
	}
}
