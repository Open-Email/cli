package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

func shareTestClient(t *testing.T, baseURL string) *coreapi.Client {
	t.Helper()
	c, err := coreapi.New(coreapi.Config{
		BaseURL:         baseURL,
		Token:           "oek_test",
		RetryBackoffMin: time.Millisecond,
		RetryBackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// captured is one request the stub server saw.
type captured struct {
	method string
	path   string
	body   map[string]any
}

func shareServer(t *testing.T, reply string, seen *[]captured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := captured{method: r.Method, path: r.URL.Path}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &rec.body)
		}
		*seen = append(*seen, rec)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
}

// Core distinguishes an OMITTED labelScope from an explicit null, and they are
// OPPOSITE requests: omitted preserves the grant's existing folders, null widens
// it to the whole mailbox. Core split them as a privilege guard for IMAP SETACL,
// whose wire format cannot express a scope at all.
//
// So "share the whole mailbox" must send an explicit null. Omitting it — the
// obvious way to write this, and the way it was first written — silently leaves
// a folder-scoped grantee scoped while reporting success.
func TestPutMailShare_WholeMailboxSendsExplicitNull(t *testing.T) {
	var seen []captured
	srv := shareServer(t, `{"mailboxId":"01MB","granteeIdentityId":"01GR","granteeAddress":"bob@x.test","rights":"lrswit","labelScope":null,"createdAt":1}`, &seen)
	defer srv.Close()

	_, err := shareTestClient(t, srv.URL).PutMailShare(
		context.Background(), "01MB", "01GR",
		coreapi.MailShareInput{Rights: "read_write", Scope: coreapi.ScopeWholeMailbox})
	if err != nil {
		t.Fatalf("PutMailShare: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected one request, got %d", len(seen))
	}
	raw, present := seen[0].body["labelScope"]
	if !present {
		t.Fatalf("whole-mailbox grant must send labelScope:null, not omit it: %v", seen[0].body)
	}
	if raw != nil {
		t.Fatalf("labelScope should be null, got %v", raw)
	}
	if seen[0].body["rights"] != "read_write" {
		t.Fatalf("rights not sent verbatim: %v", seen[0].body)
	}
	if want := "/api/v1/mailboxes/01MB/shares/01GR"; seen[0].path != want {
		t.Fatalf("path = %q, want %q", seen[0].path, want)
	}
}

// The other side of the same split: ScopePreserve is the ONLY mode that omits
// the field, and it is what `--keep-folders` asks for.
func TestPutMailShare_PreserveOmitsLabelScope(t *testing.T) {
	var seen []captured
	srv := shareServer(t, `{"mailboxId":"01MB","granteeIdentityId":"01GR","granteeAddress":null,"rights":"lrs","labelScope":["Projects"],"createdAt":1}`, &seen)
	defer srv.Close()

	_, err := shareTestClient(t, srv.URL).PutMailShare(
		context.Background(), "01MB", "01GR",
		coreapi.MailShareInput{Rights: "read_only", Scope: coreapi.ScopePreserve})
	if err != nil {
		t.Fatalf("PutMailShare: %v", err)
	}
	if _, present := seen[0].body["labelScope"]; present {
		t.Fatalf("preserve must omit labelScope, sent: %v", seen[0].body)
	}
}

func TestPutMailShare_SendsFolderNames(t *testing.T) {
	var seen []captured
	srv := shareServer(t, `{"mailboxId":"01MB","granteeIdentityId":"01GR","granteeAddress":null,"rights":"lrs","labelScope":["Projects"],"createdAt":1}`, &seen)
	defer srv.Close()

	got, err := shareTestClient(t, srv.URL).PutMailShare(
		context.Background(), "01MB", "01GR", coreapi.MailShareInput{
			Rights:  "read_only",
			Scope:   coreapi.ScopeFolders,
			Folders: []string{"Projects", "Projects/2026"},
		})
	if err != nil {
		t.Fatalf("PutMailShare: %v", err)
	}
	sent, ok := seen[0].body["labelScope"].([]any)
	if !ok || len(sent) != 2 || sent[0] != "Projects" {
		t.Fatalf("labelScope not sent as names: %v", seen[0].body)
	}
	// The RESPONSE is the authority on what was stored — core drops a scoped id
	// whose folder no longer exists, so what comes back need not equal what went.
	if len(got.LabelScope) != 1 || got.LabelScope[0] != "Projects" {
		t.Fatalf("stored scope not decoded from the response: %+v", got.LabelScope)
	}
}

// A null granteeAddress is a REAL state — an address-less identity, which core
// supports because a mailbox is a ULID-identified store and an address is
// optional. It must decode, not error, and the row must still be printable.
func TestMailShare_DecodesAddresslessGrantee(t *testing.T) {
	var seen []captured
	srv := shareServer(t, `{"shares":[{"mailboxId":"01MB","granteeIdentityId":"01GR","granteeAddress":null,"rights":"lrs","labelScope":null,"createdAt":1}]}`, &seen)
	defer srv.Close()

	shares, err := shareTestClient(t, srv.URL).ListMailShares(context.Background(), "01MB")
	if err != nil {
		t.Fatalf("ListMailShares: %v", err)
	}
	if len(shares) != 1 || shares[0].GranteeAddress != nil {
		t.Fatalf("expected one share with a null address, got %+v", shares)
	}
	if got := granteeLabel(shares[0].GranteeAddress, shares[0].GranteeIdentityID); got != "01GR" {
		t.Fatalf("granteeLabel fell back wrongly: %q", got)
	}
}

// `shared` is keyed on the PRINCIPAL and takes no mailbox: it answers for
// whoever is authenticated and cannot be pointed at anyone else.
func TestListSharedMailboxes_TakesNoMailbox(t *testing.T) {
	var seen []captured
	srv := shareServer(t, `{"sharedMailboxes":[{"mailboxId":"01OWN","granteeIdentityId":"01ME","ownerAddress":"alice@x.test","rights":"lrs","labelScope":["Projects"],"createdAt":1}]}`, &seen)
	defer srv.Close()

	shared, err := shareTestClient(t, srv.URL).ListSharedMailboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSharedMailboxes: %v", err)
	}
	if want := "/api/v1/shared-mailboxes"; seen[0].path != want {
		t.Fatalf("path = %q, want %q", seen[0].path, want)
	}
	if len(shared) != 1 || shared[0].LabelScope[0] != "Projects" {
		t.Fatalf("scope not decoded: %+v", shared)
	}
}

// The SCOPE column has to distinguish "the whole mailbox" from a folder list.
// A blank cell for the unscoped case would read as missing data in a table
// whose other rows are populated.
func TestFmtLabelScope(t *testing.T) {
	if got := fmtLabelScope(nil); got != "whole mailbox" {
		t.Fatalf("nil scope rendered %q", got)
	}
	if got := fmtLabelScope([]string{"Projects", "Travel"}); got != "Projects, Travel" {
		t.Fatalf("folder list rendered %q", got)
	}
}

// `--folder=` does NOT give cobra an empty slice — StringArray appends the
// empty string — so the guard has to look at what survives TRIMMING, not at the
// raw flag length. Written the obvious way this test fails with an auth error,
// because the grant is attempted and `[""]` goes to core as unknown_label.
func TestMailShareSet_RefusesEmptyFolderFlag(t *testing.T) {
	a := &app{}
	cmd := newMailShareSetCmd(a)
	cmd.SetArgs([]string{"bob@x.test", "--folder="})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a usage error for an empty --folder")
	}
	if !strings.Contains(err.Error(), "omit --folder to share the whole mailbox") {
		t.Fatalf("error should name the alternative, got: %v", err)
	}
}

// --keep-folders and --folder ask for opposite things; accepting both would make
// the outcome depend on which branch the switch happened to reach first.
func TestMailShareSet_RefusesKeepFoldersWithFolder(t *testing.T) {
	a := &app{}
	cmd := newMailShareSetCmd(a)
	cmd.SetArgs([]string{"bob@x.test", "--folder", "Work", "--keep-folders"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a usage error for --keep-folders with --folder")
	}
	if !strings.Contains(err.Error(), "opposite things") {
		t.Fatalf("error should explain the conflict, got: %v", err)
	}
}
