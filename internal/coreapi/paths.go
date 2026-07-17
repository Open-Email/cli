package coreapi

import "net/url"

// escapeSegment percent-escapes one path segment. do() publishes the assembled
// path as RawPath, so ids containing '@', '+', space, '%', or non-ASCII survive
// intact instead of being double-encoded or splitting the path.
func escapeSegment(s string) string { return url.PathEscape(s) }
