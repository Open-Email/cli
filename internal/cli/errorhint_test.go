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
