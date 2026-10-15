package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProfileName(t *testing.T) {
	valid := []string{
		"default",
		"prod",
		"staging-1",
		"my_profile",
		"user.name",
		"PROFILE_123",
	}
	for _, name := range valid {
		if err := ValidateProfileName(name); err != nil {
			t.Errorf("expected valid profile name %q, got error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"..",
		".",
		"../foo",
		"foo/bar",
		"foo\\bar",
		"/etc/passwd",
		"profile space",
		"profile\nname",
		"profile\x00name",
		"profile*name",
	}
	for _, name := range invalid {
		if err := ValidateProfileName(name); err == nil {
			t.Errorf("expected invalid profile name %q to fail validation, but it passed", name)
		}
	}
}

func TestSaveLoadDeleteTraversalRefusal(t *testing.T) {
	dir := t.TempDir()

	traversalProfile := "../../sensitive"
	_, _, err := Save(dir, traversalProfile, "secret-token", true)
	if err == nil {
		t.Fatalf("Save with traversal profile %q should fail", traversalProfile)
	}

	_, err = Load(dir, traversalProfile, File)
	if err == nil {
		t.Fatalf("Load with traversal profile %q should fail", traversalProfile)
	}

	err = Delete(dir, traversalProfile, File)
	if err == nil {
		t.Fatalf("Delete with traversal profile %q should fail", traversalProfile)
	}
}

func TestSaveAndLoadFileSecure(t *testing.T) {
	dir := t.TempDir()
	profile := "work-test"
	secret := "oek_secret12345"

	backend, warn, err := Save(dir, profile, secret, true)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if backend != File {
		t.Errorf("expected backend %q, got %q", File, backend)
	}
	if warn != nil {
		t.Errorf("unexpected warn: %v", warn)
	}

	// Verify file mode 0600
	credPath := filepath.Join(dir, "credentials", profile)
	fi, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 perms, got %o", fi.Mode().Perm())
	}

	loaded, err := Load(dir, profile, File)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded != secret {
		t.Errorf("expected %q, got %q", secret, loaded)
	}

	if err := Delete(dir, profile, File); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = Load(dir, profile, File)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestConcurrentSaveFileAtomic(t *testing.T) {
	dir := t.TempDir()
	profile := "concurrent-prof"
	secretA := "oek_secret_alpha_11111111111111111111"
	secretB := "oek_secret_bravo_22222222222222222222"

	// Pre-create a credential file and then concurrently write and read to verify
	// readers never observe a truncated empty file or interleaved secret.
	if _, _, err := Save(dir, profile, secretA, true); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			s := secretA
			if i%2 == 1 {
				s = secretB
			}
			if _, _, err := Save(dir, profile, s, true); err != nil {
				t.Errorf("Save(%d): %v", i, err)
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		default:
			got, err := Load(dir, profile, File)
			if err != nil {
				t.Fatalf("concurrent Load error: %v", err)
			}
			if got != secretA && got != secretB {
				t.Fatalf("concurrent Load observed corrupted/truncated secret %q", got)
			}
		}
	}
}
