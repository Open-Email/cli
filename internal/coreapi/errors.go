package coreapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// APIError is a non-2xx response from openemail-core. Core answers every error
// with the single envelope {"error":"<snake_case_code>", ...extra fields}
// (validation failures add an "issues" array; some endpoints add fields like
// maxMailboxes, flag, limit, line, col). Status is the HTTP status, Code the
// machine string. Extra holds every other top-level field verbatim so a command
// can surface it; Body is the raw response for --debug.
type APIError struct {
	Status  int
	Code    string
	Message string
	Line    int
	Col     int
	Issues  []ValidationIssue
	Extra   map[string]any
	Body    []byte
}

// ValidationIssue is one entry of the house {error:"validation_failed", issues}
// envelope produced by core's src/lib/route.ts base class.
type ValidationIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	if e.Code != "" {
		b.WriteString(e.Code)
	} else {
		b.WriteString("http_error")
	}
	fmt.Fprintf(&b, " (HTTP %d)", e.Status)
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Detail returns a multi-line human elaboration of an error: validation issues
// and any extra envelope fields. Empty when there is nothing to add.
func (e *APIError) Detail() string {
	var lines []string
	for _, iss := range e.Issues {
		where := iss.Path
		if where == "" {
			where = "(body)"
		}
		msg := iss.Message
		if msg == "" {
			msg = iss.Code
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", where, msg))
	}
	if len(e.Extra) > 0 {
		keys := make([]string, 0, len(e.Extra))
		for k := range e.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("  %s: %v", k, e.Extra[k]))
		}
	}
	return strings.Join(lines, "\n")
}

func asAPIError(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// AsAPIError exposes the typed error to callers outside the package.
func AsAPIError(err error) (*APIError, bool) { return asAPIError(err) }

// Status returns the HTTP status of an APIError, or 0.
func Status(err error) int {
	if ae, ok := asAPIError(err); ok {
		return ae.Status
	}
	return 0
}

// Code returns the machine error code of an APIError, or "".
func Code(err error) string {
	if ae, ok := asAPIError(err); ok {
		return ae.Code
	}
	return ""
}

// IsNotFound reports a 404. Core answers 404 for both genuinely missing and
// cross-tenant resources (no existence leaks), so this means "not found or not
// accessible with this key".
func IsNotFound(err error) bool { return Status(err) == 404 }

// IsUnauthorized reports a 401 (missing/bad/revoked bearer).
func IsUnauthorized(err error) bool { return Status(err) == 401 }

// IsForbidden reports a 403 (scope/policy denial).
func IsForbidden(err error) bool { return Status(err) == 403 }

// IsConflict reports a 409 (already-exists, in-use, ...). Code carries which.
func IsConflict(err error) bool { return Status(err) == 409 }

// IsValidation reports the house validation_failed envelope.
func IsValidation(err error) bool { return Code(err) == "validation_failed" }

// IsInsufficientScope reports a mailbox principal hitting a directory/admin route.
func IsInsufficientScope(err error) bool { return Code(err) == "insufficient_scope" }

// IsSystemCredentialsRequired reports a non-system principal on a system-only route.
func IsSystemCredentialsRequired(err error) bool { return Code(err) == "system_credentials_required" }

// decodeAPIError parses core's error envelope, preserving unknown fields in Extra.
func decodeAPIError(status int, body []byte) *APIError {
	ae := &APIError{Status: status, Body: body}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return ae // not the expected shape; Status alone is meaningful
	}
	if v, ok := raw["error"].(string); ok {
		ae.Code = v
	}
	if v, ok := raw["message"].(string); ok {
		ae.Message = v
	}
	ae.Line = intField(raw["line"])
	ae.Col = intField(raw["col"])
	if iss, ok := raw["issues"].([]any); ok {
		for _, it := range iss {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			ae.Issues = append(ae.Issues, ValidationIssue{
				Path:    strField(m["path"]),
				Code:    strField(m["code"]),
				Message: strField(m["message"]),
			})
		}
	}
	for _, known := range []string{"error", "message", "line", "col", "issues"} {
		delete(raw, known)
	}
	if len(raw) > 0 {
		ae.Extra = raw
	}
	return ae
}

func intField(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		i, _ := strconv.Atoi(n.String())
		return i
	}
	return 0
}

func strField(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
