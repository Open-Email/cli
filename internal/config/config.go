// Package config loads and persists the openemail CLI's user configuration:
// ~/.config/openemail/config.toml (XDG_CONFIG_HOME honored, same path on macOS —
// CLI users expect dotfile-manageable ~/.config, not ~/Library). Secrets never
// live here; only a pointer to where the key is stored (see internal/secrets).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultAPIURL is the production deployment root.
const DefaultAPIURL = "https://api.open.email"

// DefaultProfileName is used when no profile is selected.
const DefaultProfileName = "default"

// Profile is one named login. The key material itself is in the secret store;
// KeyStorage records which backend holds it ("keychain" or "file").
type Profile struct {
	APIURL         string `toml:"api_url"`
	AccountID      string `toml:"account_id,omitempty"`
	Role           string `toml:"role,omitempty"` // account | system | mailbox
	KeyStorage     string `toml:"key_storage,omitempty"`
	KeyID          string `toml:"key_id,omitempty"`   // the CLI's own key (for logout revoke)
	KeyName        string `toml:"key_name,omitempty"` // display
	DefaultMailbox string `toml:"default_mailbox,omitempty"`
}

// File is the whole config document.
type File struct {
	DefaultProfile string             `toml:"default_profile,omitempty"`
	Profiles       map[string]Profile `toml:"profiles,omitempty"`

	dir string // resolved config dir, not serialized
}

// Dir returns the config directory, creating nothing.
func Dir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: locate home: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "openemail"), nil
}

// Path returns the config file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// Load reads the config, returning an empty (but usable) document if none exists.
func Load() (*File, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	f := &File{Profiles: map[string]Profile{}, dir: dir}
	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	f.dir = dir
	return f, nil
}

// Save writes the config atomically with 0600 perms (dir 0700).
func (f *File) Save() error {
	if f.dir == "" {
		dir, err := Dir()
		if err != nil {
			return err
		}
		f.dir = dir
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", f.dir, err)
	}
	path := filepath.Join(f.dir, "config.toml")
	// A UNIQUE temp per invocation (not a fixed config.toml.tmp): two concurrent
	// CLI processes that both persist would otherwise open and interleave their
	// encoders into the same file, and the winning rename would publish a corrupt
	// document. os.CreateTemp mints an unpredictable name with 0600 perms.
	out, err := os.CreateTemp(f.dir, "config-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp in %s: %w", f.dir, err)
	}
	tmp := out.Name()
	if err := toml.NewEncoder(out).Encode(f); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("config: encode: %w", err)
	}
	if err := out.Sync(); err != nil { // durably on disk before the atomic swap
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("config: sync: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // don't leave the temp file behind on failure
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// ErrInvalidProfileName indicates a profile name contains illegal characters or path traversal.
var ErrInvalidProfileName = fmt.Errorf("config: invalid profile name")

// ValidateProfileName ensures profile names contain only safe characters ([a-zA-Z0-9_.-]).
func ValidateProfileName(name string) error {
	if name == "" || name == "." || name == ".." {
		return ErrInvalidProfileName
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return fmt.Errorf("%w: %q", ErrInvalidProfileName, name)
	}
	return nil
}

// ConfigDir returns the resolved directory the config was loaded from.
func (f *File) ConfigDir() string { return f.dir }

// Profile returns a profile and whether it exists.
func (f *File) Profile(name string) (Profile, bool) {
	if err := ValidateProfileName(name); err != nil {
		return Profile{}, false
	}
	p, ok := f.Profiles[name]
	return p, ok
}

// SetProfile upserts a profile.
func (f *File) SetProfile(name string, p Profile) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	f.Profiles[name] = p
	return nil
}

// ResolveProfileName returns the profile to use: the explicit name if non-empty,
// else the configured default, else DefaultProfileName.
func (f *File) ResolveProfileName(explicit string) string {
	if explicit != "" {
		if err := ValidateProfileName(explicit); err == nil {
			return explicit
		}
	}
	if f.DefaultProfile != "" {
		if err := ValidateProfileName(f.DefaultProfile); err == nil {
			return f.DefaultProfile
		}
	}
	return DefaultProfileName
}
