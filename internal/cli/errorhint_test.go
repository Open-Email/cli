package cli

import (
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestErrorHint(t *testing.T) {
	cases := []struct {
		name string
		ae   *coreapi.APIError
		want string // substring the hint must contain ("" = no hint)
	}{
		{
			name: "address_taken with address",
			ae:   &coreapi.APIError{Status: 409, Code: "address_taken", Extra: map[string]any{"address": "dejan@weauth.org"}},
			want: "dejan@weauth.org is already routed",
		},
		{
			name: "address_taken without address still hints",
			ae:   &coreapi.APIError{Status: 409, Code: "address_taken"},
			want: "already routed",
		},
		{
			name: "address_not_routed points at routes create",
			ae:   &coreapi.APIError{Status: 400, Code: "address_not_routed", Extra: map[string]any{"address": "alias@x.test"}},
			want: "routes create alias@x.test --type mailbox",
		},
		{
			// ONE core code, two remedies, and only the status separates them.
			// Core collapses them deliberately; the CLI is where that has to be
			// un-collapsed, or "not enabled" leaves a user with nothing to do.
			name: "semantic_not_enabled at 403 names the account gate",
			ae:   &coreapi.APIError{Status: 403, Code: "semantic_not_enabled"},
			want: "accounts update <accountId> --semantic true",
		},
		{
			name: "semantic_not_enabled at 409 names BOTH possibilities, account first",
			ae:   &coreapi.APIError{Status: 409, Code: "semantic_not_enabled"},
			want: "mailboxes update <id> --semantic true",
		},
		{
			// The remedy is an ORDER, not a flag: the tenant's opt-out is what
			// deletes the embeddings, so it has to happen before the gate clears.
			name: "semantic_mailboxes_opted_in says to opt the mailboxes out first",
			ae:   &coreapi.APIError{Status: 409, Code: "semantic_mailboxes_opted_in"},
			want: "opt each one out first",
		},
		{
			name: "semantic_capacity is not the caller's to fix",
			ae:   &coreapi.APIError{Status: 503, Code: "semantic_capacity"},
			want: "an operator adds one",
		},
		{
			// The floor rides the error, and it is the half that says what to do.
			name: "not_embedded names the window it fell outside",
			ae:   &coreapi.APIError{Status: 409, Code: "not_embedded", Extra: map[string]any{"semanticFloor": float64(1735689600)}},
			want: "--semantic-floor all",
		},
		{
			// The wait rides the envelope; without it the hint still says what
			// happened, since "run_cooldown (HTTP 429)" alone reads as a fault.
			name: "run_cooldown names the wait",
			ae:   &coreapi.APIError{Status: 429, Code: "run_cooldown", Extra: map[string]any{"retryAfter": float64(41)}},
			want: "try again in 41 s",
		},
		{
			name: "run_cooldown without a wait still hints",
			ae:   &coreapi.APIError{Status: 429, Code: "run_cooldown"},
			want: "less than a minute ago",
		},
		{
			// The end of a key's life, and the one failure the platform
			// manufactures on its own schedule: a CLI key that lapses from disuse
			// answers exactly like a revoked or mistyped one, so the hint has to
			// name the remedy rather than the cause.
			name: "401 points at login rather than leaving a dead key unexplained",
			ae:   &coreapi.APIError{Status: 401, Code: "unauthorized"},
			want: "openemail login",
		},
		{
			// Keyed on the status: core's word for a 401 is not the CLI's to
			// pick, and a deployment that answers a different code must not lose
			// the only hint that says the key needs replacing.
			name: "401 under another code still hints",
			ae:   &coreapi.APIError{Status: 401, Code: "invalid_token"},
			want: "openemail login",
		},
		{
			// The consent refusal, which is the one whose bare name tells a user
			// nothing about their own address book. `target` — not `address`,
			// which is a different key on a different family of errors.
			name: "destination_fans_out names the address and why one person matters",
			ae:   &coreapi.APIError{Status: 400, Code: "destination_fans_out", Extra: map[string]any{"target": "team@acme.dev"}},
			want: "team@acme.dev reaches more than one recipient",
		},
		{
			name: "destination_fans_out without a target still hints",
			ae:   &coreapi.APIError{Status: 400, Code: "destination_fans_out"},
			want: "more than one recipient",
		},
		{
			// Core's shared loop vocabulary: the same code answers a route, a
			// pattern, a group member and a filter rule, so the hint must read
			// on any of them.
			name: "destination_loops points at the printed chain",
			ae:   &coreapi.APIError{Status: 400, Code: "destination_loops", Extra: map[string]any{"target": "a@x.test"}},
			want: "routes back here",
		},
		{
			name: "unrelated code gets no hint",
			ae:   &coreapi.APIError{Status: 400, Code: "validation_failed"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := errorHint(tc.ae)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("expected no hint, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("hint %q does not contain %q", got, tc.want)
			}
		})
	}
}
