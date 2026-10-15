package secrets

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows' ordinary rename cannot replace a file while a reader holds it open,
// even when the reader permits delete sharing. POSIX replacement lets existing
// readers finish with the old file while new readers see the completed temp file.
func renameCredentialFile(old, new string) error {
	err := renameCredentialFileWindows(old, new)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: old, New: new, Err: err}
	}
	return nil
}

func renameCredentialFileWindows(old, new string) error {
	source, err := credentialWindowsPath(old)
	if err != nil {
		return err
	}
	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destination, err := credentialWindowsPath(new)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}

	// FILE_RENAME_INFO ends with a variable-length UTF-16 path. Keep the native
	// handle alignment on both 32-bit and 64-bit Windows, and count bytes, not runes.
	type renameInfo struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	var layout renameInfo
	nameBytes := (len(name) - 1) * 2 // Exclude the terminating NUL.
	// The Win32 API also consumes the NUL terminator when resolving this path,
	// even though FileNameLength excludes it.
	buffer := make([]byte, max(int(unsafe.Sizeof(layout)), int(unsafe.Offsetof(layout.FileName))+nameBytes+2))
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)

	handle, err := windows.CreateFile(sourceName, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	err = windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer)))
	_ = windows.CloseHandle(handle)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
		// Older Windows versions and filesystems may lack POSIX replacement.
		// Retain the ordinary atomic rename and its bounded lock retries there.
		return os.Rename(old, new)
	}
	return err
}
