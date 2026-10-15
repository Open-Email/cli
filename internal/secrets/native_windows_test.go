package secrets

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Holding our own reader open must not block a concurrent save. With os.Open's
// sharing mode this deterministically exhausts the replacement retry budget.
func TestWindowsCredentialReaderAllowsReplacement(t *testing.T) {
	dir := t.TempDir()
	const profile = "shared-reader"
	if _, _, err := Save(dir, profile, "oek_original", true); err != nil {
		t.Fatal(err)
	}
	f, err := openCredentialFile(filepath.Join(dir, "credentials", profile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, _, err := Save(dir, profile, "oek_replacement", true); err != nil {
		t.Fatalf("Save while the credential reader remains open: %v", err)
	}
	previous, err := io.ReadAll(f)
	if err != nil || string(previous) != "oek_original" {
		t.Fatalf("open reader lost its original snapshot: %q, %v", previous, err)
	}
	if got, err := Load(dir, profile, File); err != nil || got != "oek_replacement" {
		t.Fatalf("fresh reader did not see the replacement: %q, %v", got, err)
	}
}

func TestWindowsCredentialLongPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-folder\\", 25))
	if _, _, err := Save(dir, "long-path", "oek_complete", true); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(dir, "long-path", File); err != nil || got != "oek_complete" {
		t.Fatalf("long-path credential read = %q, %v", got, err)
	}
}

// Exercise actual Windows rename errors, not a stubbed error: keep the target
// open with read/write sharing but without FILE_SHARE_DELETE.
func TestWindowsCredentialReplacementWithOpenHandle(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "persistent lock"
		if release {
			name = "lock released after first rename failure"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			const profile = "native-windows"
			const oldSecret = "oek_old_credential"
			const newSecret = "oek_new_credential"
			if _, _, err := Save(dir, profile, oldSecret, true); err != nil {
				t.Fatal(err)
			}
			path, err := syscall.UTF16PtrFromString(filepath.Join(dir, "credentials", profile))
			if err != nil {
				t.Fatal(err)
			}
			handle, err := syscall.CreateFile(path, syscall.GENERIC_READ,
				syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil,
				syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			closeHandle := func() {
				if handle != syscall.InvalidHandle {
					if err := syscall.CloseHandle(handle); err != nil {
						t.Errorf("close lock handle: %v", err)
					}
					handle = syscall.InvalidHandle
				}
			}
			t.Cleanup(closeHandle)
			originalRename := renameFile
			t.Cleanup(func() { renameFile = originalRename })
			calls := 0
			renameFile = func(old, new string) error {
				calls++
				err := originalRename(old, new)
				if err != nil {
					var errno syscall.Errno
					if !errors.As(err, &errno) ||
						(errno != syscall.ERROR_ACCESS_DENIED && errno != syscall.Errno(32)) {
						t.Errorf("unexpected native rename error: %v", err)
					}
					t.Logf("native rename attempt %d failed with Windows error %d", calls, errno)
					if got, readErr := Load(dir, profile, File); readErr != nil || got != oldSecret {
						t.Errorf("old credential not preserved during retry: %q, %v", got, readErr)
					}
					if release && calls == 1 {
						closeHandle()
					}
				}
				return err
			}
			_, _, err = Save(dir, profile, newSecret, true)
			want, wantCalls := oldSecret, 5
			if release {
				want, wantCalls = newSecret, 2
				if err != nil {
					t.Errorf("Save after releasing the native lock: %v", err)
				}
			} else if err == nil {
				t.Error("Save succeeded while the native lock remained held")
			}
			if calls != wantCalls {
				t.Errorf("rename attempts = %d, want %d", calls, wantCalls)
			}
			if got, readErr := Load(dir, profile, File); readErr != nil || got != want {
				t.Errorf("stored credential = %q, %v; want %q", got, readErr, want)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "credentials"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != profile {
				t.Errorf("temporary credential not cleaned up: %v", entries)
			}
		})
	}
}

func TestWindowsCredentialReadWithOpenHandle(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "persistent lock"
		if release {
			name = "lock released after first read failure"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			const profile = "native-windows-read"
			const secret = "oek_complete_credential"
			if _, _, err := Save(dir, profile, secret, true); err != nil {
				t.Fatal(err)
			}
			path, err := syscall.UTF16PtrFromString(filepath.Join(dir, "credentials", profile))
			if err != nil {
				t.Fatal(err)
			}
			// Withhold read sharing to force a real ERROR_SHARING_VIOLATION.
			handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, 0, nil,
				syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			closeHandle := func() {
				if handle != syscall.InvalidHandle {
					if err := syscall.CloseHandle(handle); err != nil {
						t.Errorf("close lock handle: %v", err)
					}
					handle = syscall.InvalidHandle
				}
			}
			t.Cleanup(closeHandle)
			originalRead := readFile
			t.Cleanup(func() { readFile = originalRead })
			calls := 0
			readFile = func(path string) ([]byte, error) {
				calls++
				data, err := originalRead(path)
				if err != nil {
					var errno syscall.Errno
					if !errors.As(err, &errno) || errno != syscall.Errno(32) {
						t.Errorf("unexpected native read error: %v", err)
					}
					t.Logf("native read attempt %d failed with Windows error %d", calls, errno)
					if release && calls == 1 {
						closeHandle()
					}
				}
				return data, err
			}
			got, err := Load(dir, profile, File)
			wantCalls := 5
			if release {
				wantCalls = 2
				if got != secret || err != nil {
					t.Errorf("Load after releasing the native lock = %q, %v", got, err)
				}
			} else if got != "" || !errors.Is(err, syscall.Errno(32)) {
				t.Errorf("Load with persistent lock = %q, %v; want sharing violation", got, err)
			}
			if calls != wantCalls {
				t.Errorf("read attempts = %d, want %d", calls, wantCalls)
			}
			closeHandle()
			if got, err := Load(dir, profile, File); got != secret || err != nil {
				t.Errorf("credential changed while locked: %q, %v", got, err)
			}
		})
	}
}
