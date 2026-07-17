package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// readSecret reads a line from stdin without echo (hidden). Falls back to a
// plain read if stdin is not a terminal.
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return readLineRaw()
}

// readSecretRaw reads a secret WITHOUT trimming — for a password being
// established, where leading/trailing spaces are significant (unlike matching an
// existing key at login, where readSecret trims stray paste whitespace).
func readSecretRaw(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return "", sc.Err()
	}
	return sc.Text(), nil // strips the trailing newline only
}

func readLineRaw() (string, error) {
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(sc.Text()), nil
}

// confirm asks a yes/no question (default no). It refuses (returns false) when
// not on a TTY — callers pass --yes to bypass in scripts.
func confirm(prompt string) bool {
	if !promptTTY() {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	ans, err := readLineRaw()
	if err != nil {
		return false
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// confirmTyped requires the user to type expected verbatim (severe/irreversible
// actions). Refuses when not on a TTY.
func confirmTyped(prompt, expected string) bool {
	if !promptTTY() {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s\nType %q to confirm: ", prompt, expected)
	ans, err := readLineRaw()
	if err != nil {
		return false
	}
	return strings.TrimSpace(ans) == expected
}
