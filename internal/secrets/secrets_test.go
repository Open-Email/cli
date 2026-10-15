package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
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

	// Windows represents the read-only attribute here, rather than Unix
	// permission bits; access there is governed by ACLs.
	credPath := filepath.Join(dir, "credentials", profile)
	fi, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
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
	// Join the writer even after a failed read, before TempDir cleanup removes
	// its files and turns the original failure into more write errors.
	defer func() { <-done }()

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

// A locked Windows destination must never be updated in place. Retrying the
// atomic rename keeps the previous credential readable until replacement works;
// persistent locks and unrelated errors leave it untouched.
func TestWriteFileSecureRenameRetry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		windows     bool
		err         error
		failures    int
		wantCalls   int
		wantSuccess bool
	}{
		{"transient access denied", true, os.ErrPermission, 2, 3, true},
		{"transient sharing violation", true, syscall.Errno(32), 2, 3, true},
		{"persistent access denied", true, os.ErrPermission, 10, 5, false},
		{"other Windows error", true, errors.New("disk failure"), 10, 1, false},
		{"non-Windows permission error", false, os.ErrPermission, 10, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const profile = "locked"
			if _, _, err := Save(dir, profile, "oek_initial", true); err != nil {
				t.Fatal(err)
			}
			origRename, origWin := renameFile, isWindows
			t.Cleanup(func() { renameFile, isWindows = origRename, origWin })
			isWindows = tc.windows
			calls := 0
			renameFile = func(old, new string) error {
				calls++
				if got, err := Load(dir, profile, File); err != nil || got != "oek_initial" {
					t.Errorf("credential changed before replacement: %q, %v", got, err)
				}
				if calls <= tc.failures {
					return &os.LinkError{Op: "rename", Old: old, New: new, Err: tc.err}
				}
				return origRename(old, new)
			}
			_, _, err := Save(dir, profile, "oek_updated", true)
			if tc.wantSuccess {
				if err != nil {
					t.Errorf("Save: %v", err)
				}
			} else if !errors.Is(err, tc.err) {
				t.Errorf("Save error = %v, want %v", err, tc.err)
			}
			if calls != tc.wantCalls {
				t.Errorf("rename calls = %d, want %d", calls, tc.wantCalls)
			}
			want := "oek_initial"
			if tc.wantSuccess {
				want = "oek_updated"
			}
			if got, err := Load(dir, profile, File); err != nil || got != want {
				t.Errorf("credential after Save = %q, %v; want %q", got, err, want)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "credentials"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != profile {
				t.Errorf("temporary credential was not cleaned up: %v", entries)
			}
		})
	}
}

func TestLoadRetriesWindowsSharingErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		windows   bool
		err       error
		failures  int
		wantCalls int
	}{
		{"transient sharing violation", true, syscall.Errno(32), 2, 3},
		{"transient access denied", true, os.ErrPermission, 2, 3},
		{"persistent sharing violation", true, syscall.Errno(32), 10, 5},
		{"missing credential", true, os.ErrNotExist, 10, 1},
		{"other error", true, errors.New("read failure"), 10, 1},
		{"non-Windows sharing error", false, syscall.Errno(32), 10, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, _, err := Save(dir, "read-test", "oek_complete", true); err != nil {
				t.Fatal(err)
			}
			originalRead, originalWindows := readFile, isWindows
			t.Cleanup(func() { readFile, isWindows = originalRead, originalWindows })
			isWindows = tc.windows
			calls := 0
			readFile = func(path string) ([]byte, error) {
				calls++
				if calls <= tc.failures {
					return []byte("partial"), &os.PathError{Op: "open", Path: path, Err: tc.err}
				}
				return originalRead(path)
			}
			got, err := Load(dir, "read-test", File)
			if tc.failures < tc.wantCalls {
				if err != nil || got != "oek_complete" {
					t.Errorf("Load = %q, %v; want the complete credential", got, err)
				}
			} else {
				wantErr := tc.err
				if errors.Is(wantErr, os.ErrNotExist) {
					wantErr = ErrNotFound
				}
				if got != "" || !errors.Is(err, wantErr) {
					t.Errorf("Load = %q, %v; want empty token and %v", got, err, wantErr)
				}
			}
			if calls != tc.wantCalls {
				t.Errorf("read attempts = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
