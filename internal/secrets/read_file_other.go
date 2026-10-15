//go:build !windows

package secrets

import "os"

func readCredentialFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
