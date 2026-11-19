package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// exportPageSize is the listing page the export walks: core's maximum, so a
// large mailbox takes as few listing calls as possible.
const exportPageSize = 200

// exportProgressEvery is how often (in messages) a long export reports progress.
const exportProgressEvery = 1000

// exportSafeID is what a message id must look like before it becomes a file
// name. Core mints ULIDs; anything else (a path separator, "..") is refused
// rather than written somewhere outside the export directory.
var exportSafeID = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

// exportManifestLine is one line of manifest.jsonl: what was saved for one
// message, or why it could not be. Message is core's listing row as it stood
// when the export read it.
type exportManifestLine struct {
	File    string `json:"file,omitempty"`
	State   string `json:"state"`
	SHA256  string `json:"sha256,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Skipped bool   `json:"alreadyPresent,omitempty"`
	Error   string `json:"error,omitempty"`
	Message any    `json:"message"`
}

// exportSummary is export.json, and the --json output.
type exportSummary struct {
	Dir             string   `json:"dir"`
	MailboxID       string   `json:"mailboxId"`
	APIURL          string   `json:"apiUrl"`
	CLIVersion      string   `json:"cliVersion"`
	IncludeExpunged bool     `json:"includeExpunged"`
	Addresses       []string `json:"addresses,omitempty"`
	StartedAt       string   `json:"startedAt"`
	FinishedAt      string   `json:"finishedAt"`
	Live            int      `json:"live"`
	Deleted         int      `json:"deleted"`
	AlreadyPresent  int      `json:"alreadyPresent"`
	Failed          int      `json:"failed"`
	Bytes           int64    `json:"bytes"`
}

// exportItem is one listed message on its way to disk.
type exportItem struct {
	meta     coreapi.MessageMeta
	manifest any // the row written to the manifest (live or expunged shape)
}

func newMessageExportCmd(a *app) *cobra.Command {
	var (
		out             string
		includeExpunged bool
		addresses       []string
		concurrency     int
	)
	cmd := &cobra.Command{
		Use:   "export --out <dir>",
		Short: "Save every message of a mailbox as .eml files, without changing the mailbox",
		Long: `Save every message of a mailbox to a directory, one RFC 5322 .eml file per
message, named by message id. The export only reads: it never flags, moves,
restores or deletes anything, and the mailbox is exactly as it was afterwards.

  <dir>/<id>.eml           every live message (any label, Trash label included)
  <dir>/deleted/<id>.eml   with --include-expunged: messages deleted to the
                           recently-deleted tier that core still holds (they
                           are purged 14 days after the delete)
  <dir>/manifest.jsonl     one line per message: file, state, sha256, size, and
                           core's metadata (envelope, headers, labels, flags,
                           receivedAt; expungedAt/purgeAfter for deleted ones),
                           or the error that kept it from being saved
  <dir>/export.json        the run: mailbox, filters, times, counts

Every file is checked against the SHA-256 core recorded when the message was
stored; a file that does not match is not kept. Running the export again into
the same directory skips files that are already present and verify, and
replaces any that do not, so an interrupted export can simply be resumed.

--address keeps only messages where the address is the envelope sender or
recipient, or appears in From, To, Cc or Bcc (case-insensitive, exact: a
subaddress such as chris+tag@ is a different address). Repeat it for several.

Exit status is 1 when any message could not be saved (for example, purged
between the listing and the read); the manifest names each one.`,
		Example: `  openemail messages export -m support@example.com --out ./support-export
  openemail messages export -m 01J... --out ./hold --include-expunged --address chris@example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return usageError(errors.New("--out <dir> is required"))
			}
			if concurrency < 1 || concurrency > 16 {
				return usageError(errors.New("--concurrency must be between 1 and 16"))
			}
			match, err := exportAddressMatcher(addresses)
			if err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mbx, err := a.resolveMailbox(ctx, client, "")
			if err != nil {
				return err
			}
			e := &exporter{a: a, client: client, dir: out, workers: concurrency, match: match}
			e.summary = exportSummary{
				Dir: out, MailboxID: mbx, APIURL: client.BaseURL(), CLIVersion: Version,
				IncludeExpunged: includeExpunged, Addresses: match.list,
				StartedAt: time.Now().UTC().Format(time.RFC3339),
			}
			if err := e.run(ctx, mbx, includeExpunged); err != nil {
				return err
			}
			s := e.summary
			a.out.Emit(s, func(io.Writer) {
				a.out.Successf("Exported %d live and %d deleted message(s) to %s (%d bytes, %d already present)",
					s.Live, s.Deleted, s.Dir, s.Bytes, s.AlreadyPresent)
			})
			if s.Failed > 0 {
				a.out.Warnf("%d message(s) could not be exported; their lines in manifest.jsonl carry the error", s.Failed)
				return silentExit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "directory to write into (created if missing)")
	cmd.Flags().BoolVar(&includeExpunged, "include-expunged", false,
		"also save deleted messages core still holds, under <dir>/deleted/")
	cmd.Flags().StringArrayVar(&addresses, "address", nil,
		"only messages to or from this address (repeatable)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 4, "messages downloaded in parallel (1-16)")
	return cmd
}

// addressMatcher keeps the messages --address selects; with no addresses it
// keeps everything.
type addressMatcher struct {
	set  map[string]bool
	list []string
}

func exportAddressMatcher(addresses []string) (addressMatcher, error) {
	m := addressMatcher{set: map[string]bool{}}
	for _, raw := range addresses {
		addr := strings.ToLower(strings.TrimSpace(raw))
		if !strings.Contains(addr, "@") {
			return m, usageError(fmt.Errorf("--address %q is not an email address", raw))
		}
		if !m.set[addr] {
			m.set[addr] = true
			m.list = append(m.list, addr)
		}
	}
	return m, nil
}

func (m addressMatcher) keeps(meta coreapi.MessageMeta) bool {
	if len(m.set) == 0 {
		return true
	}
	hit := func(s string) bool { return m.set[strings.ToLower(strings.TrimSpace(s))] }
	if hit(meta.EnvelopeFrom) {
		return true
	}
	for _, to := range strings.Split(meta.EnvelopeTo, ",") {
		if hit(to) {
			return true
		}
	}
	for _, group := range [][]coreapi.ContentAddress{meta.From, meta.To, meta.CC, meta.BCC} {
		for _, addr := range group {
			if hit(addr.Email) {
				return true
			}
		}
	}
	return false
}

type exporter struct {
	a       *app
	client  *coreapi.Client
	dir     string
	workers int
	match   addressMatcher

	manifest *os.File
	summary  exportSummary
	seen     int
}

func (e *exporter) run(ctx context.Context, mbx string, includeExpunged bool) error {
	if err := ensurePrivateDir(e.dir); err != nil {
		return err
	}
	if includeExpunged {
		if err := ensurePrivateDir(filepath.Join(e.dir, "deleted")); err != nil {
			return err
		}
	}
	manifestPath := filepath.Join(e.dir, "manifest.jsonl")
	f, err := os.OpenFile(manifestPath+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	e.manifest = f
	defer f.Close()

	opts := coreapi.ListOpts{Limit: exportPageSize}
	if err := e.walk(ctx, "live", func(ctx context.Context, cur string) ([]exportItem, string, error) {
		page, err := e.client.ListMessages(ctx, mbx, opts.WithCursor(cur))
		items := make([]exportItem, 0, len(page.Items))
		for _, m := range page.Items {
			items = append(items, exportItem{meta: m, manifest: m})
		}
		return items, page.NextCursor, err
	}); err != nil {
		return err
	}
	if includeExpunged {
		if err := e.walk(ctx, "deleted", func(ctx context.Context, cur string) ([]exportItem, string, error) {
			page, err := e.client.ListTrash(ctx, mbx, opts.WithCursor(cur))
			items := make([]exportItem, 0, len(page.Items))
			for _, m := range page.Items {
				items = append(items, exportItem{meta: m.MessageMeta, manifest: m})
			}
			return items, page.NextCursor, err
		}); err != nil {
			return err
		}
	}

	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(manifestPath+".part", manifestPath); err != nil {
		return err
	}
	e.summary.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	body, err := json.MarshalIndent(e.summary, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(e.dir, "export.json"), append(body, '\n'))
}

// walk pages through one listing, saving each page's messages (in parallel)
// before asking for the next, so memory stays one page deep however large the
// mailbox is. Manifest lines keep the listing's order.
func (e *exporter) walk(ctx context.Context, state string,
	list func(ctx context.Context, cursor string) ([]exportItem, string, error)) error {
	cursor := ""
	for {
		items, next, err := list(ctx, cursor)
		if err != nil {
			return err
		}
		kept := items[:0]
		for _, it := range items {
			if e.match.keeps(it.meta) {
				kept = append(kept, it)
			}
		}
		lines := make([]exportManifestLine, len(kept))
		var wg sync.WaitGroup
		slots := make(chan struct{}, e.workers)
		for i := range kept {
			wg.Add(1)
			slots <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-slots }()
				lines[i] = e.save(ctx, state, kept[i])
			}(i)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		enc := json.NewEncoder(e.manifest)
		for _, l := range lines {
			if err := enc.Encode(l); err != nil {
				return err
			}
			switch {
			case l.Error != "":
				e.summary.Failed++
			case state == "live":
				e.summary.Live++
			default:
				e.summary.Deleted++
			}
			if l.Error == "" {
				e.summary.Bytes += l.Size
				if l.Skipped {
					e.summary.AlreadyPresent++
				}
			}
			e.seen++
			if e.seen%exportProgressEvery == 0 {
				e.a.out.Msgf("… %d messages", e.seen)
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

// save writes one message to disk and reports what happened as its manifest
// line. It never returns an error: a message that cannot be saved is recorded
// and the export moves on.
func (e *exporter) save(ctx context.Context, state string, it exportItem) exportManifestLine {
	line := exportManifestLine{State: state, Message: it.manifest}
	id := it.meta.ID
	if !exportSafeID.MatchString(id) {
		line.Error = "unsafe_id"
		return line
	}
	rel := id + ".eml"
	if state == "deleted" {
		rel = "deleted/" + rel
	}
	path := filepath.Join(e.dir, filepath.FromSlash(rel))

	// Resume: a file an earlier run saved intact is kept as it is.
	if it.meta.BlobHash != "" {
		if sum, size, err := hashFile(path); err == nil && sum == it.meta.BlobHash {
			line.File, line.SHA256, line.Size, line.Skipped = rel, sum, size, true
			return line
		}
	}

	rc, err := e.client.GetMessageRaw(ctx, e.summary.MailboxID, id)
	if err != nil {
		line.Error = exportErrorCode(err)
		return line
	}
	defer rc.Close()
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		line.Error = err.Error()
		return line
	}
	h := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(f, h), rc)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(part)
		line.Error = exportErrorCode(err)
		return line
	}
	line.SHA256, line.Size = hex.EncodeToString(h.Sum(nil)), size
	if it.meta.BlobHash != "" && line.SHA256 != it.meta.BlobHash {
		_ = os.Remove(part)
		line.Error = "sha256_mismatch"
		return line
	}
	if err := os.Rename(part, path); err != nil {
		_ = os.Remove(part)
		line.Error = err.Error()
		return line
	}
	line.File = rel
	return line
}

// exportErrorCode names a failed read for the manifest: core's error code when
// it answered with one, else the transport's text.
func exportErrorCode(err error) string {
	var apiErr *coreapi.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code != "" {
			return apiErr.Code
		}
		return fmt.Sprintf("http_%d", apiErr.Status)
	}
	return err.Error()
}

// ensurePrivateDir creates dir (owner-only) if it does not exist. An existing
// directory keeps the mode its owner gave it.
func ensurePrivateDir(dir string) error {
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return usageError(fmt.Errorf("%s exists and is not a directory", dir))
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func writeFileAtomic(path string, body []byte) error {
	part := path + ".part"
	if err := os.WriteFile(part, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(part, path); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}
