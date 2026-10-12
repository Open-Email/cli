package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/Open-Email/cli/internal/eventstream"
	"github.com/spf13/cobra"
)

// watchOpts bundles the convenience behaviors layered over the raw tail.
type watchOpts struct {
	until     []string // path.Match globs on the frame `type`; any match ends the watch (exit 0)
	execCmd   string   // shell command run once per event, frame JSON on stdin
	fetch     bool     // hydrate message.new/imported/updated/restored frames with GET /messages/:id
	reconnect bool
	timeout   time.Duration // 0 = no deadline
}

func newWatchCmd(a *app) *cobra.Command {
	var opts watchOpts
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Tail a mailbox's live change events over WebSocket (JSON lines to stdout)",
		Long: "Subscribe to mail and PIM change streams and print each frame as a JSON line.\n" +
			"The streams reconnect independently. A PIM store not yet provisioned is retried;\n" +
			"a refused PIM subscription is reported while mail events continue.\n" +
			"Events are signals only (identifiers, never content) — re-sync via the REST\n" +
			"commands when you see one. Ctrl-C to stop.\n\n" +
			"Conveniences:\n" +
			"  --until <glob>   exit 0 once a frame whose type matches arrives (repeatable = OR;\n" +
			"                   path.Match globs, e.g. 'message.*'). The matching frame is the\n" +
			"                   last line on stdout. Without a match the watch exits 1.\n" +
			"  --timeout <dur>  overall deadline spanning reconnects; with --until, hitting it exits 1.\n" +
			"  --exec <command> run `sh -c <command>` per event with the frame JSON on stdin\n" +
			"                   (env adds OPENEMAIL_EVENT_TYPE / OPENEMAIL_MAILBOX).\n" +
			"  --fetch          hydrate message.new/imported/updated/restored frames with message metadata\n" +
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

			matched := false
			err = eventstream.Run(ctx, eventstream.Config{APIURL: a.apiURL, Token: a.token, IdentityID: mbx, Reconnect: opts.reconnect}, func(update eventstream.Update) bool {
				if update.Frame != nil {
					matched = a.processFrame(ctx, client, mbx, opts, update.Frame)
					return matched
				}
				if update.Err != nil {
					if update.RetryIn > 0 {
						a.out.Warnf("%v — reconnecting in %s", update.Err, update.RetryIn)
					} else if update.Kind == eventstream.PIM {
						a.out.Warnf("%v — PIM events unavailable", update.Err)
					}
				}
				return false
			})
			if matched {
				return nil
			}
			if ctx.Err() != nil {
				if len(opts.until) == 0 {
					return nil
				}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return fmt.Errorf("timed out after %s waiting for %s", opts.timeout, strings.Join(opts.until, ", "))
				}
				return errors.New("watch ended without a matching event")
			}
			var failure *eventstream.Failure
			if errors.As(err, &failure) {
				if failure.Unauthorized() {
					return authRequired("not authorized to watch — check the credential or run `openemail login`")
				}
				if failure.Gone() && failure.HTTPStatus != 0 {
					return err // a refused upgrade remains an error, including HTTP 410
				}
				if failure.Gone() || failure.CloseCode == 4401 {
					if len(opts.until) > 0 {
						return errors.New("watch ended without a matching event")
					}
					if failure.Gone() {
						a.out.Msgf("identity gone — stopping")
					}
					return nil
				}
			}
			return err
		},
	}
	addMailboxFlag(cmd, a)
	cmd.Flags().BoolVar(&opts.reconnect, "reconnect", true, "auto-reconnect on session expiry / transient drops")
	cmd.Flags().StringArrayVar(&opts.until, "until", nil, "exit once a frame type matches this glob (repeatable = OR; e.g. 'message.*')")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 0, "overall deadline for the whole watch (spans reconnects)")
	cmd.Flags().StringVar(&opts.execCmd, "exec", "", "run `sh -c <command>` per event with the frame JSON on stdin")
	cmd.Flags().BoolVar(&opts.fetch, "fetch", false, "hydrate message.new/imported/updated/restored frames with message metadata (one REST call each)")
	return cmd
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
	case "message.new", "message.imported", "message.updated", "message.restored":
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

// cleanExecEnv strips sensitive credentials (such as active API tokens) before
// passing the environment to the child subshell.
func cleanExecEnv(eventType, mbx string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "OPENEMAIL_API_KEY=") || strings.HasPrefix(e, "OPENEMAIL_API_URL=") {
			continue // do not leak active credentials/redirections to arbitrary subshells
		}
		env = append(env, e)
	}
	return append(env,
		"OPENEMAIL_EVENT_TYPE="+eventType,
		"OPENEMAIL_MAILBOX="+mbx,
	)
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
	cmd.Env = cleanExecEnv(eventType, mbx)
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		a.out.Warnf("--exec handler failed: %v", err)
	}
}

// eventsURL builds the mail socket URL with the transitional PIM relay disabled.
func (a *app) eventsURL(mbx string) (string, error) {
	return eventstream.URL(a.apiURL, mbx, eventstream.Mail)
}
