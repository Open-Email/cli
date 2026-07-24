package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

func credClient(t *testing.T, handler http.HandlerFunc) (*coreapi.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := coreapi.New(coreapi.Config{BaseURL: srv.URL, Token: "oek_test", RetryBackoffMin: time.Millisecond, RetryBackoffMax: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv.Close
}

func TestBuildCredentialInput(t *testing.T) {
	// app-password never carries a password, even if one is typed by mistake.
	in, err := buildCredentialInput("app_password", "  alice@x.test ", "ignored", "  Phone ")
	if err != nil {
		t.Fatalf("app-password: %v", err)
	}
	if in.Username != "alice@x.test" || in.Password != "" || in.Name == nil || *in.Name != "Phone" {
		t.Fatalf("app-password input wrong: %+v (name=%v)", in, in.Name)
	}

	// password kind requires a non-blank password.
	if _, err := buildCredentialInput("password", "alice@x.test", "   ", ""); err == nil {
		t.Fatal("empty password must be rejected for kind=password")
	}
	in, err = buildCredentialInput("password", "alice@x.test", "s3cret", "")
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if in.Password != "s3cret" || in.Name != nil {
		t.Fatalf("password input wrong: %+v", in)
	}

	// A blank username is omitted so core defaults it to the primary address.
	in, _ = buildCredentialInput("app_password", "   ", "", "")
	if in.Username != "" {
		t.Fatalf("blank username should be omitted, got %q", in.Username)
	}
}

func TestCredKindLabel(t *testing.T) {
	if credKindLabel("app_password") != "app-password" {
		t.Fatal("app_password should render app-password")
	}
	if credKindLabel("password") != "password" {
		t.Fatal("password should pass through")
	}
}

func TestCredentialsDescRowsAndActions(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("list should GET, got %s", r.Method)
		}
		w.Write([]byte(`{"credentials":[
			{"id":"c1","kind":"app_password","username":"alice@x.test","name":"Phone","createdAt":100,"lastUsedAt":150,"revokedAt":null},
			{"id":"c2","kind":"password","username":"alice@x.test","name":null,"createdAt":90,"lastUsedAt":null,"revokedAt":200}
		]}`))
	})
	defer done()

	addr := "alice@x.test"
	mbx := coreapi.Mailbox{ID: "01M", PrimaryAddress: &addr}
	d := credentialsDesc(mbx)
	if !contains(d.name, addr) {
		t.Fatalf("title should carry the mailbox label: %q", d.name)
	}
	rows, next, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "" {
		t.Fatalf("credentials are unpaginated, got cursor %q", next)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// Kind label mapping + revoked rendering.
	if rows[0].cells[0] != "app-password" || rows[0].cells[5] != "—" {
		t.Fatalf("row0 kind/revoked wrong: %v", rows[0].cells)
	}
	if rows[1].cells[0] != "password" || rows[1].cells[5] != "yes" {
		t.Fatalf("row1 kind/revoked wrong: %v", rows[1].cells)
	}

	var newAct, revoke *action
	for i := range d.actions {
		switch d.actions[i].key {
		case "n":
			newAct = &d.actions[i]
		case "d":
			revoke = &d.actions[i]
		}
	}
	if newAct == nil || newAct.run == nil {
		t.Fatal("credentials need an n new action")
	}
	if fp := newAct.run(context.Background(), &Options{}, nil); fp == nil || !contains(fp.title(), addr) {
		t.Fatalf("n should open the create form titled for the mailbox, got %v", fp)
	}
	if revoke == nil || revoke.run == nil || !revoke.needsRow {
		t.Fatal("credentials need a row-bound d revoke action")
	}
	// Revoking an already-revoked credential is a local no-op (row c2).
	if p := revoke.run(context.Background(), &Options{}, rows[1].item); p != nil {
		t.Fatal("revoke on an already-revoked credential must be a no-op")
	}
	// A live credential opens a confirm titled with its label.
	cp := revoke.run(context.Background(), &Options{}, rows[0].item)
	if cp == nil || !contains(cp.title(), "Phone") {
		t.Fatalf("revoke on a live credential should open a confirm, got %v", cp)
	}
}

func TestMailboxCredentialsActionOpensScreen(t *testing.T) {
	var creds *action
	for i, a := range mailboxesDesc().actions {
		if a.key == "C" {
			creds = &mailboxesDesc().actions[i]
		}
	}
	if creds == nil || !creds.needsRow || creds.run == nil {
		t.Fatal("mailboxes should have a row-bound C credentials action")
	}
	addr := "bob@x.test"
	p := creds.run(context.Background(), &Options{}, coreapi.Mailbox{ID: "01M", PrimaryAddress: &addr})
	if p == nil || !contains(p.title(), "Credentials") || !contains(p.title(), addr) {
		t.Fatalf("C should open the credentials screen for the mailbox, got %v", p)
	}
}
