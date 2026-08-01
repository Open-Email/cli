package cli

import (
	"fmt"
	"io"
	"os"
)

// openRawBody yields a re-openable reader + its exact byte length for a streaming
// raw MIME upload (message APPEND, deliver outbound/inbound). Core REQUIRES a
// Content-Length (411 without), so the size must be known up front, and retries
// need a fresh reader — hence:
//
//   - a --file path is stat'd for its length and reopened per attempt;
//   - stdin (no path) is spooled to a temp file so its size is known and it can be
//     re-read; cleanup removes the temp file.
//
// cleanup is always non-nil (a no-op on the file path) so callers can defer it
// unconditionally, even on error.
func openRawBody(path string) (getBody func() (io.ReadCloser, error), length int64, cleanup func(), err error) {
	noop := func() {}
	if path != "" {
		st, serr := os.Stat(path)
		if serr != nil {
			return nil, 0, noop, serr
		}
		if st.IsDir() {
			return nil, 0, noop, fmt.Errorf("%s is a directory, not a message file", path)
		}
		get := func() (io.ReadCloser, error) { return os.Open(path) }
		return get, st.Size(), noop, nil
	}

	// stdin → temp spool.
	tmp, terr := os.CreateTemp("", "openemail-raw-*.eml")
	if terr != nil {
		return nil, 0, noop, terr
	}
	tmpPath := tmp.Name()
	rm := func() { os.Remove(tmpPath) }
	n, cerr := io.Copy(tmp, os.Stdin)
	if closeErr := tmp.Close(); cerr == nil {
		cerr = closeErr
	}
	if cerr != nil {
		rm()
		return nil, 0, noop, fmt.Errorf("spooling stdin: %w", cerr)
	}
	get := func() (io.ReadCloser, error) { return os.Open(tmpPath) }
	return get, n, rm, nil
}
