// Command openemail is the command-line client for the OpenEmail platform.
package main

import (
	"os"

	"github.com/openemail/openemail-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
