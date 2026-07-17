package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRawBodyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "msg.eml")
	content := "From: a@b\r\n\r\nhello"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	getBody, length, cleanup, err := openRawBody(path)
	if err != nil {
		t.Fatalf("openRawBody: %v", err)
	}
	defer cleanup()
	if length != int64(len(content)) {
		t.Fatalf("length: got %d want %d", length, len(content))
	}
	// getBody must be re-openable: read it twice.
	for i := 0; i < 2; i++ {
		rc, err := getBody()
		if err != nil {
			t.Fatalf("getBody #%d: %v", i, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != content {
			t.Fatalf("read #%d: got %q", i, b)
		}
	}
}

func TestOpenRawBodyDirRejected(t *testing.T) {
	_, _, cleanup, err := openRawBody(t.TempDir())
	defer cleanup()
	if err == nil {
		t.Fatal("want error for a directory path")
	}
}
