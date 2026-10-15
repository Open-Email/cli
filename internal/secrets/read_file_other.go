//go:build !windows

package secrets

import "os"

func readCredentialFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func renameCredentialFile(old, new string) error {
	return os.Rename(old, new)
}
