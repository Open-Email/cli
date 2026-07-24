package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/config"
	"github.com/coder/websocket"
)

const testMbx = "01MBXWATCHTEST00000000000"

// newWatchTestApp builds an app wired to a test server, with buffers capturing
// the raw frame stream (stdout) and status/warning lines (stderr).
func newWatchTestApp(apiURL string) (a *app, frames, stderr *bytes.Buffer) {
	frames = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	a = &app{
		apiURL: apiURL,
		token:  "test-token",
		// DefaultMailbox (not the -m flag, which cobra resets on parse) is a ULID,
		// so resolveMailbox returns it without an API call.
		profile: config.Profile{DefaultMailbox: testMbx},
		out:     &Printer{out: frames, err: stderr},
		stdout:  frames,
	}
	return a, frames, stderr
}

func runWatch(ctx context.Context, a *app, args ...string) error {
	cmd := newWatchCmd(a)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	return cmd.Execute()
}

// wsServer upgrades to a WebSocket and runs handler with the accepted conn.
func wsServer(t *testing.T, handler func(ctx context.Context, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		handler(r.Context(), conn)
	}))
}

func writeFrames(ctx context.Context, conn *websocket.Conn, frames ...string) {
	for _, f := range frames {
		if err := conn.Write(ctx, websocket.MessageText, []byte(f)); err != nil {
			return
		}
	}
}

// ── matcher ──────────────────────────────────────────────────────────────────

func TestMatchesUntil(t *testing.T) {
	cases := []struct {
		patterns []string
		event    string
		want     bool
	}{
		{[]string{"message.new"}, "message.new", true},
		{[]string{"message.*"}, "message.new", true},
		{[]string{"message.*"}, "label.created", false},
		{[]string{"label.*", "message.new"}, "message.new", true}, // OR
		{[]string{"*"}, "message.updated", true},
		{nil, "message.new", false},
	}
	for _, c := range cases {
		if got := matchesUntil(c.patterns, c.event); got != c.want {
			t.Errorf("matchesUntil(%v, %q)=%v want %v", c.patterns, c.event, got, c.want)
		}
	}
}

func TestBadUntilPatternIsUsageError(t *testing.T) {
	a, _, _ := newWatchTestApp("http://127.0.0.1:0")
	err := runWatch(context.Background(), a, "--until", "[bad")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 2 {
		t.Fatalf("bad --until pattern should be a usage error (exit 2), got %v", err)
	}
}

// ── processFrame pipeline ────────────────────────────────────────────────────

func TestProcessFramePrintAndMatch(t *testing.T) {
	a, frames, _ := newWatchTestApp("http://127.0.0.1:0")

	// A matching event: printed, reported as a match.
	if !a.processFrame(context.Background(), nil, testMbx, watchOpts{until: []string{"message.*"}},
		[]byte(`{"type":"message.new","data":{"id":"01M"}}`)) {
		t.Error("message.new should match --until message.*")
	}
	if !strings.Contains(frames.String(), `"message.new"`) {
		t.Errorf("frame not printed: %q", frames.String())
	}

	// ready is printed but never matches, even against '*'.
	frames.Reset()
	if a.processFrame(context.Background(), nil, testMbx, watchOpts{until: []string{"*"}},
		[]byte(`{"type":"ready","data":{}}`)) {
		t.Error("ready must not match --until '*'")
	}
	if !strings.Contains(frames.String(), `"ready"`) {
		t.Error("ready frame should still be printed")
	}

	// An unparseable line is printed but never matches.
	frames.Reset()
	if a.processFrame(context.Background(), nil, testMbx, watchOpts{until: []string{"*"}}, []byte(`not json`)) {
		t.Error("unparseable line must not match")
	}
	if strings.TrimSpace(frames.String()) != "not json" {
		t.Errorf("unparseable line should be printed verbatim, got %q", frames.String())
	}
}

func TestProcessFrameExec(t *testing.T) {
	a, _, _ := newWatchTestApp("http://127.0.0.1:0")
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out")

	frame := `{"type":"message.new","data":{"id":"01MSG"}}`
	// The handler appends its stdin and the two env vars to a file.
	cmd := "cat >> " + outFile + "; printf '%s|%s\\n' \"$OPENEMAIL_EVENT_TYPE\" \"$OPENEMAIL_MAILBOX\" >> " + outFile
	a.processFrame(context.Background(), nil, testMbx, watchOpts{execCmd: cmd}, []byte(frame))

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read exec output: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, frame) {
		t.Errorf("child stdin should carry the frame JSON, got %q", s)
	}
	if !strings.Contains(s, "message.new|"+testMbx) {
		t.Errorf("child env should carry OPENEMAIL_EVENT_TYPE/OPENEMAIL_MAILBOX, got %q", s)
	}
}

func TestProcessFrameExecNonZeroDoesNotStop(t *testing.T) {
	a, _, stderr := newWatchTestApp("http://127.0.0.1:0")
	// A command that exits non-zero must not panic or abort — just warn.
	matched := a.processFrame(context.Background(), nil, testMbx,
		watchOpts{execCmd: "exit 3", until: []string{"message.new"}},
		[]byte(`{"type":"message.new","data":{"id":"01M"}}`))
	if !matched {
		t.Error("until-check must still run after a failing --exec")
	}
	if !strings.Contains(stderr.String(), "handler failed") {
		t.Errorf("a non-zero exec exit should warn, stderr=%q", stderr.String())
	}
}

func TestProcessFrameFetchHydrates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages/01MSG") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"01MSG","subject":"Hello","labels":[],"flags":[],"envelopeFrom":"a@b","envelopeTo":"c@d","referencesIds":[],"receivedAt":1,"size":1,"blobHash":"h","blobGen":"g","deliveryMeta":null}`))
			return
		}
		http.Error(w, `{"error":"not_found"}`, 404)
	}))
	defer srv.Close()

	a, frames, stderr := newWatchTestApp(srv.URL)
	client, err := a.authedClient()
	if err != nil {
		t.Fatal(err)
	}

	// A hydratable frame gains a top-level "message".
	a.processFrame(context.Background(), client, testMbx, watchOpts{fetch: true},
		[]byte(`{"type":"message.new","data":{"id":"01MSG"}}`))
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(frames.String())), &out); err != nil {
		t.Fatalf("hydrated frame not valid JSON: %v (%q)", err, frames.String())
	}
	msg, ok := out["message"].(map[string]any)
	if !ok || msg["subject"] != "Hello" {
		t.Errorf("expected merged message.subject=Hello, got %v", out["message"])
	}

	// A 404 (delete race) leaves the frame unhydrated and warns.
	frames.Reset()
	stderr.Reset()
	a.processFrame(context.Background(), client, testMbx, watchOpts{fetch: true},
		[]byte(`{"type":"message.new","data":{"id":"01GONE"}}`))
	if strings.Contains(frames.String(), `"message"`) {
		t.Errorf("a failed fetch must not add a message field, got %q", frames.String())
	}
	if !strings.Contains(stderr.String(), "could not fetch") {
		t.Errorf("a failed fetch should warn, stderr=%q", stderr.String())
	}
}

// ── websocket-driven exit behavior ───────────────────────────────────────────

func TestWatchUntilExitsZero(t *testing.T) {
	srv := wsServer(t, func(ctx context.Context, conn *websocket.Conn) {
		writeFrames(ctx, conn, `{"type":"ready","data":{}}`, `{"type":"message.new","data":{"id":"01M"}}`)
		conn.Close(websocket.StatusNormalClosure, "")
	})
	defer srv.Close()

	a, frames, _ := newWatchTestApp(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWatch(ctx, a, "--until", "message.new"); err != nil {
		t.Fatalf("--until match should exit 0, got %v", err)
	}
	// The matching frame is guaranteed to be the last stdout line.
	lines := strings.Split(strings.TrimSpace(frames.String()), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, `"message.new"`) {
		t.Errorf("matching frame should be the last stdout line, got %q", last)
	}
}

func TestWatchUntilTimeoutExitsOne(t *testing.T) {
	srv := wsServer(t, func(ctx context.Context, conn *websocket.Conn) {
		writeFrames(ctx, conn, `{"type":"ready","data":{}}`)
		<-ctx.Done() // hold the socket open, never send a match
	})
	defer srv.Close()

	a, _, _ := newWatchTestApp(srv.URL)
	err := runWatch(context.Background(), a, "--until", "message.new", "--timeout", "250ms")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("--until + timeout without a match should error 'timed out', got %v", err)
	}
}

func TestWatchReadyDoesNotSatisfyUntilStar(t *testing.T) {
	srv := wsServer(t, func(ctx context.Context, conn *websocket.Conn) {
		writeFrames(ctx, conn, `{"type":"ready","data":{}}`)
		<-ctx.Done()
	})
	defer srv.Close()

	a, frames, _ := newWatchTestApp(srv.URL)
	// If `ready` matched '*' this would exit 0 immediately; instead it must run to
	// the deadline (proving ready is excluded).
	err := runWatch(context.Background(), a, "--until", "*", "--timeout", "250ms")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("ready must not satisfy --until '*'; expected timeout, got %v", err)
	}
	if !strings.Contains(frames.String(), `"ready"`) {
		t.Error("ready should have been printed before the deadline")
	}
}

func TestWatchReconnectsThenMatches(t *testing.T) {
	var dials atomic.Int32
	srv := wsServer(t, func(ctx context.Context, conn *websocket.Conn) {
		switch dials.Add(1) {
		case 1:
			// First session: hand out a ready, then a 4401 session-expiry close.
			writeFrames(ctx, conn, `{"type":"ready","data":{}}`)
			conn.Close(4401, "session_expired")
		default:
			// After reconnect: deliver the awaited event.
			writeFrames(ctx, conn, `{"type":"message.new","data":{"id":"01M"}}`)
			conn.Close(websocket.StatusNormalClosure, "")
		}
	})
	defer srv.Close()

	a, _, _ := newWatchTestApp(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWatch(ctx, a, "--until", "message.new"); err != nil {
		t.Fatalf("watch should reconnect after 4401 and match, got %v", err)
	}
	if dials.Load() < 2 {
		t.Errorf("expected a reconnect (≥2 dials), got %d", dials.Load())
	}
}
