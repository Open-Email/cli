package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Open-Email/cli/internal/coreapi"
)

// prefsDesc shows one mailbox's client-preferences document: the opaque JSON
// blob core stores and never interprets, where the webmail and other clients
// keep their settings.
//
// It is a VIEWER plus one destructive verb. Authoring belongs to the clients
// that own the keys — a console that offered to edit arbitrary JSON values
// would be a worse text editor than the CLI's `prefs set` — but reading the
// document, and dropping a key a retired client left behind, are exactly what
// someone opens this screen to do.
func prefsDesc(mbx coreapi.Mailbox) resourceDesc {
	st := &prefsScreenState{}
	return resourceDesc{
		key:  "prefs:" + mbx.ID,
		name: "Preferences — " + mailboxLabel(&mbx),
		summary: func(w int) []string {
			return st.summaryLines(w)
		},
		columns: []column{
			{title: "KEY", width: 24},
			{title: "TYPE", width: 8},
			{title: "VALUE", flex: true},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			// An unwritten document is NOT a 404 — core answers the blob with
			// `version: 0`. The only 404 here is a mailbox that is gone or no
			// longer reachable with this key, so it must surface rather than be
			// dressed up as an empty screen.
			p, err := c.GetPrefs(ctx, mbx.ID)
			if err != nil {
				return nil, "", err
			}
			st.set(p)
			keys := make([]string, 0, len(p.Prefs))
			for k := range p.Prefs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			rows := make([]rowData, len(keys))
			for i, k := range keys {
				v := p.Prefs[k]
				rows[i] = rowData{
					cells: []string{k, prefKind(v), prefInline(v)},
					item:  prefRow{key: k, value: v},
				}
			}
			return rows, "", nil // the document is a single object
		},
		detail: func(item any) []kv {
			pr := item.(prefRow)
			kvs := []kv{
				{k: "key", v: pr.key},
				{k: "type", v: prefKind(pr.value)},
				{},
				{v: "Value"},
			}
			for _, line := range strings.Split(prefPretty(pr.value), "\n") {
				kvs = append(kvs, kv{v: line})
			}
			return kvs
		},
		actions: []action{
			{key: "d", label: "d remove key", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return prefKeyDeleteConfirm(ctx, ui, mbx, item.(prefRow))
			}},
		},
	}
}

// prefRow is one top-level key of the document.
type prefRow struct {
	key   string
	value any
}

// prefsScreenState is written by the fetch goroutine and read by the render
// loop — see sieveScreenState for why that needs a mutex.
type prefsScreenState struct {
	mu     sync.Mutex
	loaded bool
	prefs  *coreapi.Prefs
}

func (s *prefsScreenState) set(p *coreapi.Prefs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.prefs = true, p
}

func (s *prefsScreenState) summaryLines(w int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return nil
	}
	if s.prefs == nil {
		return nil
	}
	if s.prefs.Version == 0 {
		// Version 0 IS the never-written state, reported by core rather than
		// inferred from an absent document.
		return []string{stDim.Render(truncate("no preferences written yet — core stores but never interprets these", w))}
	}
	return []string{stDim.Render(truncate(fmt.Sprintf(
		"opaque client settings · version %d · core stores but never interprets these",
		s.prefs.Version), w))}
}

// prefKind names a JSON value's type. Go decodes every number as float64, so an
// integral value is reported as "number" rather than pretending to know more.
func prefKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "?"
}

// prefInline renders a value on one table row — compact JSON, so a nested
// object is still recognizable rather than shown as a Go struct dump.
func prefInline(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func prefPretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// prefKeyDeleteConfirm removes one key with the version just read as the CAS
// guard, so a concurrent write from another device is refused rather than
// silently reverted by this read-modify-write.
func prefKeyDeleteConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, pr prefRow) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Remove preference " + pr.key,
		body: "Deletes this key from the document. Core does not interpret preferences, so " +
			"the effect depends entirely on the client that wrote it — a client still using " +
			"the key will fall back to its default, or re-create it on next save.",
		verb: "remove key",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			cur, err := c.GetPrefs(sctx, mbx.ID)
			if err != nil {
				return "", err
			}
			if _, present := cur.Prefs[pr.key]; !present {
				return "", fmt.Errorf("%q is no longer in the document — refresh", pr.key)
			}
			next := make(map[string]any, len(cur.Prefs))
			for k, v := range cur.Prefs {
				if k != pr.key {
					next[k] = v
				}
			}
			p, err := c.PutPrefs(sctx, mbx.ID, next, fmt.Sprintf("%d", cur.Version))
			if err != nil {
				if ae, ok := coreapi.AsAPIError(err); ok && ae.Status == 412 {
					return "", fmt.Errorf("these preferences changed while you were looking — refresh and retry")
				}
				return "", err
			}
			return fmt.Sprintf("%s removed (version %d)", pr.key, p.Version), nil
		},
	})
}
