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
