package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	f, err := Load()
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if len(f.Profiles) != 0 {
		t.Fatalf("want empty, got %+v", f.Profiles)
	}

	f.SetProfile("default", Profile{
		APIURL:     "http://localhost:8787",
		AccountID:  "acc1",
		Role:       "account",
		KeyStorage: "file",
		KeyID:      "k1",
		KeyName:    "device",
	})
	f.DefaultProfile = "default"
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Config file exists at the XDG path with 0600.
	path := filepath.Join(dir, "openemail", "config.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	g, err := Load()
	if err != nil {
		t.Fatalf("Load again: %v", err)
	}
	p, ok := g.Profile("default")
	if !ok || p.AccountID != "acc1" || p.KeyStorage != "file" || p.KeyID != "k1" {
		t.Fatalf("round-trip mismatch: %+v", p)
	}
	if g.ResolveProfileName("") != "default" {
		t.Fatalf("ResolveProfileName default failed")
	}
	if g.ResolveProfileName("other") != "other" {
		t.Fatalf("ResolveProfileName explicit failed")
	}
}
