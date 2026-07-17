//go:build integration

// Package integration drives the built openemail binary end-to-end against a
// local `wrangler dev` core seeded with the dev keys. Run with:
//
//	make integration          # (core must be running on :8787, migrated + seeded)
//	go test -tags integration ./test/...
//
// It is deliberately separate from the fast unit tests (no build tag) so a plain
// `go test ./...` never needs a live backend.
package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	accountKey = "oek_dev_account_localtest_do_not_use"
	systemKey  = "oek_dev_system_localtest_do_not_use"
	aliceID    = "01DEVMAILBOXALICE0000000AA"
	aliceAddr  = "alice@example.test"
)

func apiURL() string {
	if v := os.Getenv("OPENEMAIL_API_URL"); v != "" {
		return v
	}
	return "http://localhost:8787"
}

// sharedBin is the binary built once by TestMain.
var sharedBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "openemail-itest-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	sharedBin = filepath.Join(dir, "openemail")
	build := exec.Command("go", "build", "-o", sharedBin, "../cmd/openemail")
	if out, berr := build.CombinedOutput(); berr != nil {
		panic("build: " + berr.Error() + "\n" + string(out))
	}
	os.Exit(m.Run())
}

// harness runs the built binary with an isolated config + the given key.
type harness struct {
	t   *testing.T
	bin string
	cfg string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Skip unless the dev core is reachable.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(apiURL() + "/api/v1/health")
	if err != nil {
		t.Skipf("core not reachable at %s (%v) — start `npm run dev` and seed", apiURL(), err)
	}
	resp.Body.Close()
	// Config lives in its own dir, never colliding with the binary name.
	return &harness{t: t, bin: sharedBin, cfg: filepath.Join(t.TempDir(), "cfg")}
}

func (h *harness) run(key string, args ...string) (string, string, int) {
	h.t.Helper()
	cmd := exec.Command(h.bin, args...)
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+h.cfg,
		"OPENEMAIL_NO_KEYRING=1",
		"OPENEMAIL_NO_UPDATE_NOTIFIER=1",
		"OPENEMAIL_API_URL="+apiURL(),
		"OPENEMAIL_API_KEY="+key,
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

func (h *harness) json(key string, args ...string) map[string]any {
	h.t.Helper()
	out, errb, code := h.run(key, append([]string{"--json"}, args...)...)
	if code != 0 {
		h.t.Fatalf("cmd %v exit %d: %s", args, code, errb)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		h.t.Fatalf("decode %v: %v\n%s", args, err, out)
	}
	return m
}

func TestLifecycle(t *testing.T) {
	h := newHarness(t)

	// Create a throwaway mailbox.
	mb := h.json(accountKey, "mailboxes", "create")
	id, _ := mb["id"].(string)
	if id == "" {
		t.Fatal("no mailbox id")
	}
	defer h.run(accountKey, "mailboxes", "delete", id, "--purge", "--yes")

	// Append a message from stdin.
	appendCmd := exec.Command(h.bin, "messages", "append", "-m", id,
		"--envelope-from", "a@x.test", "--envelope-to", "b@y.test")
	appendCmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+h.cfg, "OPENEMAIL_NO_KEYRING=1", "OPENEMAIL_NO_UPDATE_NOTIFIER=1",
		"OPENEMAIL_API_URL="+apiURL(), "OPENEMAIL_API_KEY="+accountKey)
	appendCmd.Stdin = strings.NewReader("From: a@x.test\r\nSubject: itest\r\n\r\nbody\r\n")
	if out, err := appendCmd.CombinedOutput(); err != nil {
		t.Fatalf("append: %v\n%s", err, out)
	}

	// List: exactly one message.
	list := h.json(accountKey, "messages", "list", "-m", id)
	msgs, _ := list["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}

	// Labels list includes the system inbox.
	labels := h.json(accountKey, "labels", "list", "-m", id)
	if _, ok := labels["labels"].([]any); !ok {
		t.Fatal("labels missing")
	}
}

func TestDeliverCheckAndAdmin(t *testing.T) {
	h := newHarness(t)

	// RCPT check: alice accepted, nobody rejected (exit 1).
	if _, _, code := h.run(accountKey, "deliver", "check", "--to", aliceAddr); code != 0 {
		t.Fatalf("alice should be accepted, exit %d", code)
	}
	if _, _, code := h.run(accountKey, "deliver", "check", "--to", "nobody@example.test"); code == 0 {
		t.Fatal("unknown recipient should exit non-zero")
	}

	// admin verify-login with the system key.
	v := h.json(systemKey, "admin", "verify-login", aliceAddr, "--password", "devpassword123")
	if v["mailboxId"] != aliceID {
		t.Fatalf("verify-login mailboxId = %v", v["mailboxId"])
	}

	// account key is refused system-only routes.
	if _, _, code := h.run(accountKey, "admin", "reindex", aliceID); code == 0 {
		t.Fatal("account key should be refused admin reindex")
	}
}
