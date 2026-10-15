package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/config"
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
	now := time.Now()
	loc := now.Location()
	y, m, d := now.Date()
	// Pick a timestamp later today if at least 2 minutes remain before midnight,
	// and a timestamp 2 hours into tomorrow (< 24h away if run after 02:00) to
	// verify calendar-day comparison does not mislabel tomorrow as "(today)".
	endOfToday := time.Date(y, m, d, 23, 59, 0, 0, loc)
	tomorrowNoon := time.Date(y, m, d+1, 1, 0, 0, 0, loc)

	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"a lapse weeks past is stated as past", now.Add(-30 * 24 * time.Hour), "(lapsed)"},
		{"a lapse hours ago is past too", now.Add(-3 * time.Hour), "(lapsed)"},
		{"a lapse early tomorrow (<24h away) says tomorrow, not today", tomorrowNoon, "(tomorrow, unless used)"},
		{"a distant lapse counts the days", time.Date(y, m, d+45, 12, 0, 0, 0, loc), "(in 45 days, unless used)"},
	}
	if endOfToday.After(now.Add(2 * time.Minute)) {
		cases = append(cases, struct {
			name string
			when time.Time
			want string
		}{"a lapse still to come today says today", endOfToday, "(today)"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderIdentity(t, coreapi.Principal{
				Type: coreapi.PrincipalAccount, IdleExpiresAt: tc.when.Unix(),
			})
			if !strings.Contains(out, tc.want) {
				t.Fatalf("want the lapse described as %q:\n%s", tc.want, out)
			}
			if !strings.Contains(out, "Lapses") || !strings.Contains(out, tc.when.Format("2006-01-02")) {
				t.Fatalf("want a Lapses row dated %s:\n%s", tc.when.Format("2006-01-02"), out)
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

func TestWhoamiJSONIncludesDefaultMailboxAndNullVerificationToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/auth/whoami") {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "system"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var outBuf bytes.Buffer
	a := &app{
		out:         &Printer{json: true, out: &outBuf, err: &bytes.Buffer{}},
		profileName: "default",
		profile:     config.Profile{APIURL: srv.URL, DefaultMailbox: "01J00000000000000000000MBX"},
		apiURL:      srv.URL,
		token:       "oek_sys",
		tokenSource: "profile",
	}
	cmd := newWhoamiCmd(a)
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal whoami json: %v", err)
	}
	if payload["defaultMailbox"] != "01J00000000000000000000MBX" {
		t.Fatalf("defaultMailbox = %v, want 01J00000000000000000000MBX", payload["defaultMailbox"])
	}
	if val, exists := payload["domainVerificationToken"]; !exists || val != nil {
		t.Fatalf("domainVerificationToken = %v (exists=%v), want nil", val, exists)
	}
}
