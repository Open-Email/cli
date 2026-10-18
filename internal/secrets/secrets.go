// Package secrets stores the CLI's bearer tokens, preferring the OS keychain and
// falling back — loudly — to a 0600 file. The config records which backend holds
// a given profile's key so Load/Delete target the right one.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/zalando/go-keyring"
)

var (
	renameFile = renameCredentialFile
	readFile   = readCredentialFile
	isWindows  = runtime.GOOS == "windows"
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
	if err := ValidateProfileName(profile); err != nil {
		return "", nil, err
	}
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

// writeFileSecure writes data to path atomically with 0600, refusing to follow a
// symlink or clobber a non-regular file, and enforcing 0600 even when the file
// already exists.
func writeFileSecure(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("refusing to write %s: not a regular file", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".cred-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := replaceCredentialFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Windows scanners and readers can temporarily prevent replacement by holding
// the destination without FILE_SHARE_DELETE. Retry the completed temp file;
// never truncate the live credential, even if the lock does not clear.
func replaceCredentialFile(tmp, path string) error {
	return retryCredentialAccess(func() error { return renameFile(tmp, path) })
}

// Reads can also meet a sharing violation while another process replaces the
// credential. Use the same bounded retry policy for both operations, preserving
// permanent errors and the missing-credential result without a delay.
func retryCredentialAccess(operation func() error) error {
	// ERROR_SHARING_VIOLATION is 32 on Windows. The numeric value keeps the
	// retry path testable on other platforms; it is only interpreted on Windows.
	const sharingViolation = syscall.Errno(32)
	for attempt := 0; ; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}
		if !isWindows || attempt == 4 ||
			(!errors.Is(err, os.ErrPermission) && !errors.Is(err, sharingViolation)) {
			return err
		}
		// Five total attempts, with at most 375 ms of backoff.
		time.Sleep((25 * time.Millisecond) << attempt)
	}
}

// On Windows, a concurrent open can also briefly observe ERROR_FILE_NOT_FOUND
// while Save swaps the destination link. Retry not-found up to twice when the
// credential directory shows an active (.cred-*) or just-completed replacement,
// while still returning ErrNotFound immediately when no credential exists.
func retryCredentialRead(path string, read func(string) ([]byte, error)) ([]byte, error) {
	const sharingViolation = syscall.Errno(32)
	for attempt := 0; ; attempt++ {
		data, err := read(path)
		if err == nil {
			return data, nil
		}
		if !isWindows || attempt == 4 {
			return nil, err
		}
		switch {
		case errors.Is(err, os.ErrPermission), errors.Is(err, sharingViolation):
		case os.IsNotExist(err) && attempt < 2 && credentialReplaceInFlight(path):
		default:
			return nil, err
		}
		time.Sleep((25 * time.Millisecond) << attempt)
	}
}

func credentialReplaceInFlight(path string) bool {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	target := filepath.Base(path)
	for _, entry := range entries {
		name := entry.Name()
		if name == target || strings.HasPrefix(name, ".cred-") {
			return true
		}
	}
	return false
}

// Load reads the secret for profile from the named backend.
func Load(configDir, profile, backend string) (string, error) {
	if err := ValidateProfileName(profile); err != nil {
		return "", err
	}
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
		data, err := retryCredentialRead(path, readFile)
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
	if err := ValidateProfileName(profile); err != nil {
		return err
	}
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

// ErrInvalidProfileName is returned when a profile name fails safety validation.
var ErrInvalidProfileName = errors.New("secrets: invalid profile name")

// ValidateProfileName validates that a profile name is safe to use in keychains and paths.
// It allows alphanumeric characters, underscores, hyphens, and dots, rejecting directory traversal
// and path separators.
func ValidateProfileName(profile string) error {
	if profile == "" || profile == "." || profile == ".." {
		return ErrInvalidProfileName
	}
	for i := 0; i < len(profile); i++ {
		c := profile[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return fmt.Errorf("%w: %q contains invalid characters", ErrInvalidProfileName, profile)
	}
	return nil
}

func filePath(configDir, profile string) (string, error) {
	if configDir == "" {
		return "", errors.New("secrets: empty config dir")
	}
	if err := ValidateProfileName(profile); err != nil {
		return "", err
	}
	return filepath.Join(configDir, "credentials", profile), nil
}
