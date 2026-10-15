package secrets

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readCredentialFile(path string) ([]byte, error) {
	f, err := openCredentialFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// The standard Windows os.Open denies delete sharing, so a steady stream of
// credential reads can block replacement until every rename retry is exhausted.
// Allow replacement while this handle continues reading the previous file.
func openCredentialFile(path string) (*os.File, error) {
	full, err := credentialWindowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	name, err := syscall.UTF16PtrFromString(full)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func credentialWindowsPath(path string) (string, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Match os.ReadFile's support for long paths even on Windows installations
	// where extended-length paths require an explicit prefix.
	if len(full) >= 248 && !strings.HasPrefix(full, `\\?\`) && !strings.HasPrefix(full, `\\.\`) {
		if strings.HasPrefix(full, `\\`) {
			full = `\\?\UNC\` + full[2:]
		} else {
			full = `\\?\` + full
		}
	}
	return full, nil
}
