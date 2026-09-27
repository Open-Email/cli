package cli

import (
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestDescribeNotFound(t *testing.T) {
	const mb = "01KXRS3SHN1N35G4YETVADSN0R"
	cases := []struct {
		path    string
		subject string   // "" = no subject line
		hints   []string // substrings, each must appear in some hint
		noHint  string   // substring that must NOT appear
	}{
		{
			// The user's case: a name where an id goes. Core's bare 404 becomes
			// "that is not an id", which is the actual mistake.
			path:    "/mailboxes/alice",
			subject: `no mailbox "alice"`,
			hints:   []string{`"alice" does not look like a mailbox id`, "openemail mailboxes list", "may not be allowed to see it"},
		},
		{
			path:    "/mailboxes/alice@example.com",
			subject: `no mailbox "alice@example.com"`,
			hints:   []string{"is an address, but a mailbox is named by its id"},
		},
		{
			path:    "/mailboxes/" + strings.ToLower(mb),
			subject: `no mailbox "` + strings.ToLower(mb) + `"`,
			hints:   []string{"did you mean " + mb},
		},
		{
			// A nested path names the innermost resource, in its parent.
			path:    "/mailboxes/" + mb + "/messages/foo",
			subject: `no message "foo" in mailbox "` + mb + `"`,
			hints:   []string{`"foo" does not look like a message id`, "openemail messages list"},
			noHint:  mb + `" does not look`,
		},
		{
			// A well-formed id gets no shape hint: it may simply be someone else's.
			path:    "/mailboxes/" + mb,
			subject: `no mailbox "` + mb + `"`,
			hints:   []string{"may not be allowed to see it"},
			noHint:  "does not look like",
		},
		{
			// A verb after the collection is a route, not an id — the mailbox is
			// the thing that was not found.
			path:    "/mailboxes/" + mb + "/messages/compose",
			subject: `no mailbox "` + mb + `"`,
		},
		{
			path:    "/mailboxes/" + mb + "/labels",
			subject: `no mailbox "` + mb + `"`,
		},
		{
			// Labels are user-named, so a word that is a route elsewhere is still
			// a label here.
			path:    "/mailboxes/" + mb + "/labels/check",
			subject: `no label "check" in mailbox "` + mb + `"`,
		},
		{
			path:    "/routes/bob",
			subject: `no route for address "bob"`,
			hints:   []string{"does not look like an email address"},
		},
		{
			path:    "/domains/alice@example.com/dns",
			subject: `no domain "alice@example.com"`,
			hints:   []string{"the domain is example.com"},
		},
		{
			path:    "/patterns/forty",
			subject: `no pattern "forty"`,
			hints:   []string{`"forty" is not a number`},
		},
		{
			// Nothing this table knows: say what was asked so the reader can see
			// the path, instead of pretending to know what it names.
			path:   "/nonsense/route",
			hints:  []string{"GET /nonsense/route answered 404"},
			noHint: "may not be allowed",
		},
	}
	for _, tc := range cases {
		ae := &coreapi.APIError{Status: 404, Code: "not_found", Method: "GET", Path: tc.path}
		subject, hints := describeNotFound(ae)
		if subject != tc.subject {
			t.Errorf("%s: subject %q, want %q", tc.path, subject, tc.subject)
		}
		joined := strings.Join(hints, "\n")
		for _, h := range tc.hints {
			if !strings.Contains(joined, h) {
				t.Errorf("%s: hints %q lack %q", tc.path, joined, h)
			}
		}
		if tc.noHint != "" && strings.Contains(joined, tc.noHint) {
			t.Errorf("%s: hints %q must not contain %q", tc.path, joined, tc.noHint)
		}
	}
}

// An error that never reached the wire has no path, and describing it must
// not invent one.
func TestDescribeNotFoundWithoutPath(t *testing.T) {
	subject, hints := describeNotFound(&coreapi.APIError{Status: 404, Code: "not_found"})
	if subject != "" || len(hints) != 0 {
		t.Errorf("got %q %q, want nothing", subject, hints)
	}
}
