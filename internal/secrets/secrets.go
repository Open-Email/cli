// Package secrets stores the CLI's bearer tokens, preferring the OS keychain and
// falling back — loudly — to a 0600 file. The config records which backend holds
// a given profile's key so Load/Delete target the right one.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Backends.
const (
	Keychain = "keychain"
	File     = "file"
)

// keyringService namespaces our keychain entries.
const keyringService = "openemail-cli"

// ErrNotFound is returned when no secret is stored for the profile.
var ErrNotFound = errors.New("secrets: not found")

// Save stores secret for profile. Unless forceFile is set it tries the OS
// keychain first; on any keychain error (or when forceFile is set) it falls back
// to a 0600 file under <configDir>/credentials and returns backend File. A
// keychain error yields a non-nil warn describing the downgrade (the caller
// surfaces it — a silent plaintext fallback is a security footgun); an explicit
// forceFile is not a downgrade, so warn is nil there. On success via the
// keychain, warn is nil.
func Save(configDir, profile, secret string, forceFile bool) (backend string, warn error, err error) {
	if !forceFile {
		if kerr := keyring.Set(keyringService, profile, secret); kerr == nil {
			return Keychain, nil, nil
		} else {
			warn = fmt.Errorf("OS keychain unavailable (%v); storing key in a 0600 file instead", kerr)
		}
	}
	path, ferr := filePath(configDir, profile)
	if ferr != nil {
		return "", warn, ferr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", warn, fmt.Errorf("secrets: mkdir: %w", err)
	}
	if err := writeFileSecure(path, []byte(secret)); err != nil {
		return "", warn, fmt.Errorf("secrets: write: %w", err)
	}
	return File, warn, nil
}

// writeFileSecure writes data to path with 0600, refusing to follow a symlink or
// clobber a non-regular file, and enforcing 0600 even when the file already
// exists (os.WriteFile only applies its mode on creation).
func writeFileSecure(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("refusing to write %s: not a regular file", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// Load reads the secret for profile from the named backend.
func Load(configDir, profile, backend string) (string, error) {
	switch backend {
	case Keychain:
		s, err := keyring.Get(keyringService, profile)
		if err != nil {
			if errors.Is(err, keyring.ErrNotFound) {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("secrets: keychain get: %w", err)
		}
		return s, nil
	case File:
		path, err := filePath(configDir, profile)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("secrets: read: %w", err)
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("secrets: unknown backend %q", backend)
	}
}

// Delete removes the secret for profile from the named backend (idempotent).
func Delete(configDir, profile, backend string) error {
	switch backend {
	case Keychain:
		if err := keyring.Delete(keyringService, profile); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("secrets: keychain delete: %w", err)
		}
		return nil
	case File:
		path, err := filePath(configDir, profile)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("secrets: remove: %w", err)
		}
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("secrets: unknown backend %q", backend)
	}
}

func filePath(configDir, profile string) (string, error) {
	if configDir == "" {
		return "", errors.New("secrets: empty config dir")
	}
	return filepath.Join(configDir, "credentials", profile), nil
}
