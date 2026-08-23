package cli

import (
	"os/exec"
	"runtime"
)

// openBrowserFn is the seam the login tests replace — a browser is the one part
// of this flow that cannot be exercised for real, and every other part of it can.
var openBrowserFn = openBrowser

// openBrowser asks the desktop to open url, returning whether a handler was
// started. Best-effort by design: every caller also PRINTS the URL, because the
// case this fails in — a remote shell, a container, a stripped desktop — is
// exactly the case where the human needs to copy it somewhere else.
//
// Deliberately not github.com/pkg/browser: this is three platform commands, and
// the dependency would be a supply-chain surface on the one code path that
// handles a login.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// rundll32 rather than `cmd /c start`: start treats `&` in a URL as a
		// command separator, and these URLs are query strings full of them.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	// Reap the child rather than leaving a zombie for the life of the process —
	// `watch` can run for hours after a login in the same invocation.
	go func() { _ = cmd.Wait() }()
	return true
}
