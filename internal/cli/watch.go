package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// watchOpts bundles the convenience behaviors layered over the raw tail.
type watchOpts struct {
	until     []string // path.Match globs on the frame `type`; any match ends the watch (exit 0)
	execCmd   string   // shell command run once per event, frame JSON on stdin
	fetch     bool     // hydrate message.new/updated/restored frames with GET /messages/:id
	reconnect bool
	timeout   time.Duration // 0 = no deadline
}

func newWatchCmd(a *app) *cobra.Command {
	var opts watchOpts
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Tail a mailbox's live change events over WebSocket (JSON lines to stdout)",
		Long: "Subscribe to a mailbox's live event stream and print each frame as a JSON line.\n" +
			"Events are signals only (identifiers, never content) — re-sync via the REST\n" +
			"commands when you see one. Ctrl-C to stop.\n\n" +
			"Conveniences:\n" +
			"  --until <glob>   exit 0 once a frame whose type matches arrives (repeatable = OR;\n" +
			"                   path.Match globs, e.g. 'message.*'). The matching frame is the\n" +
			"                   last line on stdout. Without a match the watch exits 1.\n" +
			"  --timeout <dur>  overall deadline spanning reconnects; with --until, hitting it exits 1.\n" +
			"  --exec <command> run `sh -c <command>` per event with the frame JSON on stdin\n" +
			"                   (env adds OPENEMAIL_EVENT_TYPE / OPENEMAIL_MAILBOX).\n" +
			"  --fetch          hydrate message.new/updated/restored frames with message metadata\n" +
			"                   (one REST call per event — prefer a plain watch on a hot mailbox).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Validate --until globs up front so a typo fails fast (exit 2) rather
			// than silently never matching.
			for _, p := range opts.until {
				if _, err := path.Match(p, "x"); err != nil {
					return usageError(fmt.Errorf("bad --until pattern %q: %w", p, err))
				}
			}

			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			// Stop cleanly on Ctrl-C; --timeout layers a deadline over the same ctx
			// so it spans dials, reconnects, and backoff sleeps.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if opts.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, opts.timeout)
				defer cancel()
			}

			a.out.Msgf("watching mailbox %s — events are signals only; re-sync via REST. Ctrl-C to stop.", mbx)

			const backoffFloor = 500 * time.Millisecond
			backoff := backoffFloor
			for {
				start := time.Now()
				retryable, matched, cerr := a.watchOnce(ctx, client, mbx, opts)
				if matched {
					return nil // --until satisfied
				}
				if ctx.Err() != nil {
					// The watch ended because the context ended.
					if len(opts.until) == 0 {
						return nil // plain tail / bounded tail: Ctrl-C or deadline is a clean stop
					}
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						return fmt.Errorf("timed out after %s waiting for %s", opts.timeout, strings.Join(opts.until, ", "))
					}
					return errors.New("watch ended without a matching event")
				}
				if !opts.reconnect || !retryable {
					if cerr != nil {
						return cerr
					}
					if len(opts.until) > 0 {
						return errors.New("watch ended without a matching event")
					}
					return nil
				}
				// A session that stayed up (e.g. an expected 12h 4401 expiry) is not
				// a failure — reset the backoff so the re-dial is prompt and only
				// genuinely rapid, repeated failures escalate it.
				if time.Since(start) >= 30*time.Second {
					backoff = backoffFloor
				}
				if cerr != nil {
					a.out.Warnf("connection lost (%v) — reconnecting in %s", cerr, backoff)
				}
				select {
				case <-ctx.Done():
					continue // let the ctx.Err() branch above classify the stop
				case <-time.After(backoff):
				}
				if backoff < 15*time.Second {
					backoff *= 2
				}
			}
		},
	}
	addMailboxFlag(cmd, a)
	cmd.Flags().BoolVar(&opts.reconnect, "reconnect", true, "auto-reconnect on session expiry / transient drops")
	cmd.Flags().StringArrayVar(&opts.until, "until", nil, "exit once a frame type matches this glob (repeatable = OR; e.g. 'message.*')")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 0, "overall deadline for the whole watch (spans reconnects)")
	cmd.Flags().StringVar(&opts.execCmd, "exec", "", "run `sh -c <command>` per event with the frame JSON on stdin")
	cmd.Flags().BoolVar(&opts.fetch, "fetch", false, "hydrate message.new/updated/restored frames with message metadata (one REST call each)")
	return cmd
}

// watchOnce opens one WebSocket session and streams frames until it closes or a
// frame matches --until. Returns whether a reconnect is warranted, whether an
// --until match ended it, and any terminal error.
func (a *app) watchOnce(ctx context.Context, client *coreapi.Client, mbx string, opts watchOpts) (retryable, matched bool, err error) {
	wsURL, uerr := a.eventsURL(mbx)
	if uerr != nil {
		return false, false, uerr
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, resp, derr := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.token}},
	})
	if derr != nil {
		// Pre-accept refusals are plain HTTP statuses. Deterministic 4xx must NOT
		// be retried (a bad bearer would otherwise reconnect forever); only 429 and
		// transport/5xx are retryable.
		if resp != nil {
			switch resp.StatusCode {
			case 401:
				return false, false, authRequired("not authorized to watch this mailbox — check the credential or run `openemail login`")
			case 404:
				return false, false, errors.New("mailbox not found or not accessible with this key")
			case 410:
				return false, false, errors.New("mailbox is being deleted — stopping")
			case 426:
				return false, false, errors.New("server refused the upgrade (426)")
			case 429:
				return true, false, errors.New("too many subscribers (429)")
			}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return false, false, fmt.Errorf("watch refused (HTTP %d)", resp.StatusCode)
			}
		}
		return true, false, derr
	}
	defer conn.CloseNow()

	// Keepalive: v1 is one-way; the client sends only "ping" (the runtime
	// auto-answers "pong" without waking the DO) to keep the socket healthy.
	pingCtx, pingStop := context.WithCancel(ctx)
	defer pingStop()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				if werr := conn.Write(pingCtx, websocket.MessageText, []byte("ping")); werr != nil {
					return
				}
			}
		}
	}()

	conn.SetReadLimit(1 << 20)
	for {
		typ, data, rerr := conn.Read(ctx)
		if rerr != nil {
			switch websocket.CloseStatus(rerr) {
			case 4401:
				a.out.Warnf("session expired — reconnecting with a fresh session")
				return true, false, nil
			case 4403:
				// credential_revoked: core says do NOT reconnect with this bearer.
				return false, false, authRequired("credential revoked — re-authenticate with `openemail login`")
			case 4410:
				a.out.Msgf("mailbox gone — stopping")
				return false, false, nil
			}
			return true, false, rerr // transient / network drop
		}
		if typ != websocket.MessageText {
			continue
		}
		line := strings.TrimSpace(string(data))
		if line == "" || line == "pong" {
			continue // keepalive echo, not an event
		}
		if a.processFrame(ctx, client, mbx, opts, []byte(line)) {
			// Close the socket cleanly before returning — a normal closure, not a drop.
			conn.Close(websocket.StatusNormalClosure, "")
			return false, true, nil
		}
	}
}

// processFrame handles one event line: (optional) hydrate, print, (optional)
// exec, then report whether it satisfies --until. Order is fixed: hydrate →
// print → exec → until-check, so a script's `--exec` always sees the same JSON
// that was printed, and the matching frame is guaranteed to be the last stdout
// line. `ready` handshakes (re-fired on every reconnect) and unparseable lines
// are printed but never hydrated, exec'd, or matched.
func (a *app) processFrame(ctx context.Context, client *coreapi.Client, mbx string, opts watchOpts, line []byte) bool {
	var f struct {
		Type string `json:"type"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	parsed := json.Unmarshal(line, &f) == nil
	eventful := parsed && f.Type != "ready" // a real change event, not a handshake

	out := line
	if opts.fetch && eventful && f.Data.ID != "" && hydratable(f.Type) {
		if merged, ok := a.hydrateFrame(ctx, client, mbx, line, f.Data.ID); ok {
			out = merged
		}
	}
	sink := a.stdout
	if sink == nil {
		sink = os.Stdout
	}
	fmt.Fprintln(sink, string(out))

	if opts.execCmd != "" && eventful {
		a.runExec(ctx, opts.execCmd, out, f.Type, mbx)
	}

	return eventful && matchesUntil(opts.until, f.Type)
}

func hydratable(eventType string) bool {
	switch eventType {
	case "message.new", "message.updated", "message.restored":
		return true
	}
	return false
}

func matchesUntil(patterns []string, eventType string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, eventType); ok { // patterns pre-validated in RunE
			return true
		}
	}
	return false
}

// hydrateFrame merges the message's metadata into the frame as a top-level
// "message" field. Best-effort: on any error (incl. a 404 from a delete race) it
// warns and leaves the frame unhydrated, so `message` is present iff the fetch
// succeeded.
func (a *app) hydrateFrame(ctx context.Context, client *coreapi.Client, mbx string, line []byte, id string) ([]byte, bool) {
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	meta, err := client.GetMessage(fetchCtx, mbx, id)
	if err != nil {
		a.out.Warnf("could not fetch message %s: %v", id, err)
		return line, false
	}
	var frame map[string]any
	if err := json.Unmarshal(line, &frame); err != nil {
		return line, false
	}
	frame["message"] = meta
	merged, err := json.Marshal(frame)
	if err != nil {
		return line, false
	}
	return merged, true
}

// runExec runs the handler synchronously with the frame JSON on stdin. Its
// stdout and stderr are both wired to the CLI's stderr so stdout stays pure
// JSON-lines. A non-zero exit is logged and ignored — events are best-effort, so
// a failing handler never kills the tail. The child runs under the watch ctx, so
// Ctrl-C also interrupts a hung handler.
func (a *app) runExec(ctx context.Context, command string, frame []byte, eventType, mbx string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdin = bytes.NewReader(append(frame, '\n'))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"OPENEMAIL_EVENT_TYPE="+eventType,
		"OPENEMAIL_MAILBOX="+mbx,
	)
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		a.out.Warnf("--exec handler failed: %v", err)
	}
}

// eventsURL builds the ws(s):// upgrade URL for a mailbox's events channel.
func (a *app) eventsURL(mbx string) (string, error) {
	u, err := url.Parse(strings.TrimRight(a.apiURL, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	// mbx is a resolved ULID (URL-safe); set Path and let url.String() escape once
	// (PathEscape here would double-encode any special char via String()).
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/mailboxes/" + mbx + "/events"
	return u.String(), nil
}
