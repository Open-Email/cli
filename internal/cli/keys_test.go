package cli

import (
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// epochIn is an instant d from now in the shape core sends it: nullable epoch
// seconds.
func epochIn(d time.Duration) *int64 { return ptr(time.Now().Add(d).Unix()) }

// STATUS is what a listing of keys is read for — which of these still work —
// and the only column decided here rather than reported from core.
func TestKeyStatusReportsWhetherAKeyStillWorks(t *testing.T) {
	future := epochIn(30 * 24 * time.Hour)
	cases := []struct {
		name string
		key  coreapi.APIKey
		want string
	}{
		{"a key with no lapse is active", coreapi.APIKey{}, "active"},
		{"a lapse ahead is a date to act on", coreapi.APIKey{IdleExpiresAt: future}, "lapses " + fmtEpochPtr(future)},
		// Hours rather than days: a lapse a few hours old is the case where a
		// day count truncates to zero and the key reads as one with time left.
		{"a lapse hours ago has already happened", coreapi.APIKey{IdleExpiresAt: epochIn(-3 * time.Hour)}, "lapsed"},
		// Revocation is the more definite of the two ends, and it is also the
		// one somebody DID. A revoked key reported as lapsing next month invites
		// the reader to wait for a deadline that means nothing.
		{"revocation outranks a lapse still to come", coreapi.APIKey{RevokedAt: epochIn(-time.Hour), IdleExpiresAt: future}, "revoked"},
		{"revocation outranks a lapse already past", coreapi.APIKey{RevokedAt: epochIn(-time.Hour), IdleExpiresAt: epochIn(-3 * time.Hour)}, "revoked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyStatus(tc.key); got != tc.want {
				t.Fatalf("status %q, want %q", got, tc.want)
			}
		})
	}
}
