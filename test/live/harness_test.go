//go:build live

// Package live is the black-box live e2e suite for the openemail CLI. It builds
// the real binary and drives it end-to-end against a DEPLOYED openemail-core
// worker over HTTPS — the CLI analogue of core's own test/live suite. Where the
// core suite asserts raw HTTP status + error strings, this one asserts what the
// CLI actually surfaces: exit codes, the `--json` payloads on stdout, and the
// machine error codes it prints to stderr.
//
// It is gated on OE_HOST + OE_SYSTEM_KEY (see ./README.md) and behind the `live`
// build tag, so a plain `go test ./...` (and `make test` / `make integration`)
// never touches a live backend. Run:
//
//	make live               # OE_HOST + OE_SYSTEM_KEY must be set
//	go test -tags live ./test/...
//
// Provisioning uses the SYSTEM key (accounts create, cross-tenant reads, admin,
// and send-disabled outbound all need it) exactly as core's harness does. Every
// object is tagged with a per-run id so a crashed run's residue stays sweepable.
package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- env & run identity ----------------------------------------------------

var (
	liveHost   string // OE_HOST — worker origin, e.g. https://oe.acme.workers.dev
	systemKey  string // OE_SYSTEM_KEY — a role='system' oek_ token
	d1Database string // OE_D1_DATABASE — D1 db name for teardown
	skipD1     bool   // OE_SKIP_D1_CLEANUP=1 — leave the wrangler sweep undone

	binPath string // the openemail binary built once by TestMain
	cfgDir  string // an isolated XDG_CONFIG_HOME (never a real profile)
	runID   string // per-run tag; every object is named/addressed with it
)

const envFrom = "sender@example.com"

func liveEnabled() bool { return liveHost != "" && systemKey != "" }

func requireLive(t *testing.T) {
	t.Helper()
	if !liveEnabled() {
		t.Skip("OE_HOST / OE_SYSTEM_KEY not set — see test/live/README.md")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	liveHost = strings.TrimRight(os.Getenv("OE_HOST"), "/")
	systemKey = os.Getenv("OE_SYSTEM_KEY")
	d1Database = envOr("OE_D1_DATABASE", "openemail-directory")
	skipD1 = os.Getenv("OE_SKIP_D1_CLEANUP") == "1"

	if !liveEnabled() {
		fmt.Fprintln(os.Stderr,
			"[cli live] OE_HOST / OE_SYSTEM_KEY not set — skipping. See test/live/README.md.")
		os.Exit(m.Run()) // every test self-skips
	}

	// Randomized so two isolated runs starting in the same millisecond don't
	// collide on the globally-unique domain namespace. Lowercase alphanumerics
	// only — these become DNS labels.
	runID = strings.ToLower(fmt.Sprintf("cli%s%s",
		strconv.FormatInt(time.Now().UnixNano(), 36),
		strconv.FormatInt(rand.Int63n(1_000_000), 36)))

	dir, err := os.MkdirTemp("", "openemail-live-*")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "openemail")
	cfgDir = filepath.Join(dir, "cfg")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/openemail")
	if out, berr := build.CombinedOutput(); berr != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", berr, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	teardown()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- running the binary ----------------------------------------------------

// execCLI runs the built binary with an isolated config and the given bearer.
// stdin (when non-empty) is fed to the process. Returns stdout, stderr, and the
// exit code; a code of -1 means the process could not be launched. It never uses
// *testing.T so teardown (which has none) can call it too.
func execCLI(key, stdin string, args ...string) (string, string, int) {
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+cfgDir,
		"OPENEMAIL_NO_KEYRING=1",
		"OPENEMAIL_NO_UPDATE_NOTIFIER=1",
		"OPENEMAIL_API_URL="+liveHost,
		"OPENEMAIL_API_KEY="+key,
		"NO_COLOR=1",
	)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return out.String(), errb.String(), -1
	}
	return out.String(), errb.String(), code
}

// runAs runs the binary and fatals only if the process failed to launch (a
// non-zero exit is a normal, inspectable outcome).
func runAs(t *testing.T, key, stdin string, args ...string) (string, string, int) {
	t.Helper()
	out, errb, code := execCLI(key, stdin, args...)
	if code == -1 {
		t.Fatalf("exec %v: could not launch\n%s", args, errb)
	}
	return out, errb, code
}

// jsonAs runs the binary with --json as a specific bearer, requires a clean exit,
// and decodes stdout into a generic map.
func jsonAs(t *testing.T, key, stdin string, args ...string) map[string]any {
	t.Helper()
	out, errb, code := runAs(t, key, stdin, append([]string{"--json"}, args...)...)
	if code != 0 {
		t.Fatalf("cmd %v exit %d\nstderr: %s", args, code, errb)
	}
	return decodeMap(t, args, out)
}

// sysJSON is jsonAs with the system key and no stdin — the everyday provisioning
// and inspection call.
func sysJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	return jsonAs(t, systemKey, "", args...)
}

// expectFail runs the CLI as `key`, requires a non-zero exit, and asserts stderr
// carries `want` — a machine error code the CLI echoes (e.g. "insufficient_scope",
// "unknown_address") or an HTTP status ("404", "409"). Pass want="" to assert only
// that the command failed. Returns stderr for any further inspection.
func expectFail(t *testing.T, key, want string, args ...string) string {
	t.Helper()
	_, errb, code := runAs(t, key, "", args...)
	if code == 0 {
		t.Fatalf("cmd %v unexpectedly succeeded (wanted failure with %q)", args, want)
	}
	if want != "" && !strings.Contains(errb, want) {
		t.Fatalf("cmd %v stderr = %q, want it to contain %q", args, strings.TrimSpace(errb), want)
	}
	return errb
}

func decodeMap(t *testing.T, args []string, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode %v: %v\nstdout: %s", args, err, out)
	}
	return m
}

// --- provisioning (system key) ---------------------------------------------

type registry struct {
	primaryAccount string
	accountIDs     []string
	mailboxIDs     []string
	domains        []string
	groupRoutes    []string
}

var reg registry

// ensureAccount lazily mints the primary (first) account and caches it.
func ensureAccount(t *testing.T) string {
	if reg.primaryAccount == "" {
		reg.primaryAccount = createAccount(t, "primary")
	}
	return reg.primaryAccount
}

// createAccount mints a fresh account, added to the teardown sweep. Use for a
// SECOND, isolated tenant.
func createAccount(t *testing.T, label string) string {
	m := sysJSON(t, "accounts", "create", fmt.Sprintf("e2e-live %s %s", runID, label))
	id := str(m, "id")
	if id == "" {
		t.Fatalf("account create returned no id: %v", m)
	}
	reg.accountIDs = append(reg.accountIDs, id)
	return id
}

type domainOpts struct {
	accountID string
	enabled   *bool
	// sendVerified is the EARNED half of sendability (core 0062 replaced the old
	// canSend). A system key may assert it outright, which is what lets a case
	// here mint a domain that cannot send without going near a DNS check.
	sendVerified *bool
	// sendOnly is the INVERSE of the old canReceive: a send-only domain keeps
	// its inbound MX elsewhere, so nothing on it is a local recipient.
	sendOnly *bool
	aliasOf  string
}

// createDomain provisions a throwaway `.test` domain. Booleans default to
// enabled/sendVerified = true and sendOnly = false (matching core's live
// harness), so the caller only overrides what a case needs.
func createDomain(t *testing.T, o domainOpts) string {
	acct := o.accountID
	if acct == "" {
		acct = ensureAccount(t)
	}
	domain := fmt.Sprintf("%s-%d.test", runID, len(reg.domains))
	args := []string{
		"domains", "create", domain, "--account", acct,
		boolFlag("--enabled", boolOr(o.enabled, true)),
		boolFlag("--send-verified", boolOr(o.sendVerified, true)),
		boolFlag("--send-only", boolOr(o.sendOnly, false)),
	}
	if o.aliasOf != "" {
		args = append(args, "--alias-of", o.aliasOf)
	}
	sysJSON(t, args...)
	reg.domains = append(reg.domains, domain)
	return domain
}

type mailboxOpts struct {
	accountID  string
	quotaBytes int64
}

// createMailbox provisions a mailbox (optionally with a primary address, quota,
// or a non-primary account).
func createMailbox(t *testing.T, primaryAddress string, o mailboxOpts) string {
	acct := o.accountID
	if acct == "" {
		acct = ensureAccount(t)
	}
	args := []string{"mailboxes", "create", "--account", acct}
	if primaryAddress != "" {
		args = append(args, "--address", primaryAddress)
	}
	if o.quotaBytes > 0 {
		args = append(args, "--quota", strconv.FormatInt(o.quotaBytes, 10))
	}
	m := sysJSON(t, args...)
	id := str(m, "id")
	if id == "" {
		t.Fatalf("mailbox create returned no id: %v", m)
	}
	reg.mailboxIDs = append(reg.mailboxIDs, id)
	return id
}

func createRoute(t *testing.T, address, mailboxID string) {
	sysJSON(t, "routes", "create", address, "--type", "mailbox", "--mailbox", mailboxID)
}

func createGroupRoute(t *testing.T, address string, members []string) {
	sysJSON(t, "routes", "create", address, "--type", "group")
	reg.groupRoutes = append(reg.groupRoutes, address)
	sysJSON(t, append([]string{"routes", "members", "replace", address}, members...)...)
}

func createPattern(t *testing.T, domain, pattern, mailboxID string) {
	sysJSON(t, "patterns", "create",
		"--domain", domain, "--pattern", pattern, "--type", "mailbox", "--mailbox", mailboxID)
}

// createAppPassword mints an app_password credential (a mailbox principal),
// returning its login username and the one-time plaintext token.
func createAppPassword(t *testing.T, mailboxID string) (username, token string) {
	m := sysJSON(t, "credentials", "create", mailboxID, "--kind", "app-password")
	username, token = str(m, "username"), str(m, "token")
	if token == "" {
		t.Fatalf("credential create returned no token: %v", m)
	}
	return username, token
}

// createAccountKey mints an account-scoped API key (an account principal) for the
// given tenant; the row is removed by the per-account D1 sweep.
func createAccountKey(t *testing.T, accountID string) string {
	m := sysJSON(t, "keys", "create", fmt.Sprintf("e2e key %s", runID),
		"--role", "account", "--account", accountID)
	tok := str(m, "token")
	if tok == "" {
		t.Fatalf("key create returned no token: %v", m)
	}
	return tok
}

// --- delivery --------------------------------------------------------------

// deliver injects one inbound message through the routing ladder as the system
// key, returning stdout/stderr/exit-code. Callers that expect success parse the
// stdout JSON; error cases assert on the exit code + the stderr error string.
func deliver(t *testing.T, to, body, id string) (string, string, int) {
	t.Helper()
	return runAs(t, systemKey, body, "--json", "deliver", "inbound",
		"--to", to, "--from", envFrom, "--delivery-id", id)
}

// deliverOK injects a message and requires success, returning the parsed result.
func deliverOK(t *testing.T, to, body, id string) map[string]any {
	t.Helper()
	out, errb, code := deliver(t, to, body, id)
	if code != 0 {
		t.Fatalf("deliver to %s exit %d\nstderr: %s", to, code, errb)
	}
	return decodeMap(t, []string{"deliver", to}, out)
}

// --- polling & data helpers ------------------------------------------------

func poll(t *testing.T, desc string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, desc)
}

// subjectsIn returns the subjects currently in a mailbox's ordinary (live)
// listing.
func subjectsIn(t *testing.T, mailboxID string) []string {
	t.Helper()
	m := sysJSON(t, "messages", "list", "-m", mailboxID)
	return subjectsOf(m, "messages")
}

// subjectsOf pulls the "subject" of every element of the named array field.
func subjectsOf(m map[string]any, arrayKey string) []string {
	var out []string
	for _, it := range arr(m, arrayKey) {
		if mm, ok := it.(map[string]any); ok {
			if s, ok := mm["subject"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// findByID returns the array element whose "id" equals id, or nil.
func findByID(m map[string]any, arrayKey, id string) map[string]any {
	for _, it := range arr(m, arrayKey) {
		if mm, ok := it.(map[string]any); ok {
			if str(mm, "id") == id {
				return mm
			}
		}
	}
	return nil
}

func arr(m map[string]any, key string) []any {
	a, _ := m[key].([]any)
	return a
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// num reads a JSON number field (all JSON numbers decode to float64).
func num(m map[string]any, key string) (float64, bool) {
	f, ok := m[key].(float64)
	return f, ok
}

func labelNames(m map[string]any) []string {
	var out []string
	for _, it := range arr(m, "labels") {
		if mm, ok := it.(map[string]any); ok {
			if n := str(mm, "name"); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// labelUID returns the per-label UID of `name` in a message-metadata object
// (its .labels is an array of {name, uid}).
func labelUID(m map[string]any, name string) (float64, bool) {
	for _, it := range arr(m, "labels") {
		if mm, ok := it.(map[string]any); ok && str(mm, "name") == name {
			return num(mm, "uid")
		}
	}
	return 0, false
}

// metaField reads a string field out of a message's nested deliveryMeta object.
func metaField(m map[string]any, key string) string {
	meta, _ := m["deliveryMeta"].(map[string]any)
	return str(meta, key)
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func boolFlag(name string, v bool) string { return fmt.Sprintf("%s=%t", name, v) }

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func boolp(v bool) *bool { return &v }

// mime builds a minimal RFC822 message (CRLF, a Message-ID, plain body).
func mime(from, to, subject, body string) string {
	if body == "" {
		body = "Body for " + subject
	}
	mid := fmt.Sprintf("%s.%d@example.com", runID, time.Now().UnixNano())
	return strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"Message-ID: <" + mid + ">",
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
		"",
	}, "\r\n")
}

// uniq is a monotonic-ish suffix for unique subjects/ids within a run.
func uniq() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63n(1_000_000))
}

func deliveryID(tag string) string {
	return fmt.Sprintf("%s-%s-%s", runID, tag, uniq())
}

// --- teardown --------------------------------------------------------------

// teardown mirrors core's two-phase reclaim: soft-delete every mailbox (which
// arms each DO's deadman + closes admission — the only path that schedules the
// deferred store/blob wipe), remove group routes, then sweep the account rows +
// tombstones the API can't reach out of D1 with wrangler.
func teardown() {
	for _, id := range reg.mailboxIDs {
		execCLI(systemKey, "", "mailboxes", "delete", id, "--yes")
	}
	for _, addr := range reg.groupRoutes {
		execCLI(systemKey, "", "routes", "delete", addr, "--yes")
	}

	if len(reg.accountIDs) == 0 {
		return
	}
	if skipD1 {
		fmt.Fprintf(os.Stderr,
			"[cli live] OE_SKIP_D1_CLEANUP=1 — leaving accounts %s and tombstones in D1.\n",
			strings.Join(reg.accountIDs, ", "))
		return
	}
	if err := d1Sweep(reg.accountIDs); err != nil {
		fmt.Fprintf(os.Stderr,
			"[cli live] wrangler D1 cleanup failed (tests already ran). Accounts %s + tombstones may remain. Error: %v\n",
			strings.Join(reg.accountIDs, ", "), err)
	}
}

// d1Sweep removes each run account's directory rows, mailbox tombstones, and the
// account itself, in child→parent order. Best-effort: it shells out to wrangler,
// which must be authenticated against the account that owns the DB.
func d1Sweep(accountIDs []string) error {
	var b strings.Builder
	for _, accountID := range accountIDs {
		a := strings.ReplaceAll(accountID, "'", "''")
		fmt.Fprintf(&b, "UPDATE domains SET alias_of=NULL WHERE account_id='%s';\n", a)
		fmt.Fprintf(&b, "DELETE FROM route_members WHERE address IN (SELECT address FROM routes WHERE domain IN (SELECT domain FROM domains WHERE account_id='%s'));\n", a)
		fmt.Fprintf(&b, "DELETE FROM routes WHERE domain IN (SELECT domain FROM domains WHERE account_id='%s') OR mailbox_id IN (SELECT id FROM mailboxes WHERE account_id='%s');\n", a, a)
		fmt.Fprintf(&b, "DELETE FROM route_patterns WHERE domain IN (SELECT domain FROM domains WHERE account_id='%s') OR mailbox_id IN (SELECT id FROM mailboxes WHERE account_id='%s');\n", a, a)
		fmt.Fprintf(&b, "DELETE FROM mailbox_credentials WHERE mailbox_id IN (SELECT id FROM mailboxes WHERE account_id='%s');\n", a)
		fmt.Fprintf(&b, "DELETE FROM api_keys WHERE account_id='%s';\n", a)
		fmt.Fprintf(&b, "DELETE FROM mailboxes WHERE account_id='%s';\n", a)
		fmt.Fprintf(&b, "DELETE FROM deleted_mailboxes WHERE account_id='%s';\n", a)
		fmt.Fprintf(&b, "DELETE FROM domains WHERE account_id='%s';\n", a)
		fmt.Fprintf(&b, "DELETE FROM accounts WHERE id='%s';\n", a)
	}

	dir, err := os.MkdirTemp("", "oe-live-cleanup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "cleanup.sql")
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		return err
	}

	// Prefer a wrangler on PATH; fall back to `npx wrangler`.
	var cmd *exec.Cmd
	if path, lookErr := exec.LookPath("wrangler"); lookErr == nil {
		cmd = exec.Command(path, "d1", "execute", d1Database, "--remote", "--file="+file)
	} else {
		cmd = exec.Command("npx", "wrangler", "d1", "execute", d1Database, "--remote", "--file="+file)
	}
	cmd.Stdin = strings.NewReader("y\n") // confirm if a wrangler version prompts
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}
