package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// renderIdentity renders whoami's human view for one principal.
func renderIdentity(t *testing.T, id coreapi.Principal) string {
	t.Helper()
	a := &app{
		out:         newPrinter(false, true),
		profileName: "default",
		apiURL:      "https://api.test",
		tokenSource: "profile",
	}
	var buf bytes.Buffer
	a.printIdentity(&buf, id, "")
	return buf.String()
}

// The lapse is the one line of whoami somebody acts on, and its TENSE is the
// whole of it: a key that stopped working three hours ago, announced as one
// that lapses today, is a key nobody replaces — they read it as still working
// and go looking for the 401 somewhere else.
func TestWhoamiSaysWhetherTheKeyHasAlreadyLapsed(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration // when the lapse falls, from now
		want string
	}{
		{"a lapse weeks past is stated as past", -30 * 24 * time.Hour, "(lapsed)"},
		// Hours rather than days, because the countdown truncates toward zero:
		// this key is "0 days" out and dead, and both facts have to survive.
		{"a lapse hours ago is past too", -3 * time.Hour, "(lapsed)"},
		{"a lapse still to come today says today", 3 * time.Hour, "(today)"},
		{"a distant lapse counts the days", 45*24*time.Hour + time.Hour, "(in 45 days, unless used)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			when := time.Now().Add(tc.in)
			out := renderIdentity(t, coreapi.Principal{
				Type: coreapi.PrincipalAccount, IdleExpiresAt: when.Unix(),
			})
			if !strings.Contains(out, tc.want) {
				t.Fatalf("want the lapse described as %q:\n%s", tc.want, out)
			}
			// The date is what somebody acts on; the words after it are what
			// makes them read it. Neither is any use alone.
			if !strings.Contains(out, "Lapses") || !strings.Contains(out, when.Format("2006-01-02")) {
				t.Fatalf("want a Lapses row dated %s:\n%s", when.Format("2006-01-02"), out)
			}
		})
	}
}

// Most keys never lapse — every one minted outside the browser login. A row for
// them would invent a deadline core never set.
func TestWhoamiInventsNoLapseForAKeyThatHasNone(t *testing.T) {
	out := renderIdentity(t, coreapi.Principal{Type: coreapi.PrincipalAccount})
	if strings.Contains(out, "Lapses") {
		t.Fatalf("a key that never lapses was given a lapse:\n%s", out)
	}
}
