package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestParseTimeArg(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1751000000", 1751000000, true},
		{"2026-07-01T00:00:00Z", 1782864000, true},
		{"2026-07-01", 1782864000, true},
		{"yesterday", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, err := parseTimeArg(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("parseTimeArg(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parseTimeArg(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseLifetime(t *testing.T) {
	if d, err := parseLifetime("90d"); err != nil || d != 90*24*time.Hour {
		t.Errorf("90d = %v, %v", d, err)
	}
	if d, err := parseLifetime("12h"); err != nil || d != 12*time.Hour {
		t.Errorf("12h = %v, %v", d, err)
	}
	for _, bad := range []string{"d", "-3d", "0d", "soon"} {
		if _, err := parseLifetime(bad); err == nil {
			t.Errorf("parseLifetime(%q) unexpectedly parsed", bad)
		}
	}
}

func TestNormalizePartstat(t *testing.T) {
	cases := map[string]string{
		"accepted":     "ACCEPTED",
		"Declined":     "DECLINED",
		"TENTATIVE":    "TENTATIVE",
		"needs-action": "NEEDS-ACTION",
		"needs_action": "NEEDS-ACTION",
	}
	for in, want := range cases {
		got, err := normalizePartstat(in)
		if err != nil || got != want {
			t.Errorf("normalizePartstat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizePartstat("maybe"); err == nil {
		t.Error("normalizePartstat(maybe) unexpectedly ok")
	}
}

func TestPimHrefArg(t *testing.T) {
	if href, err := pimHrefArg([]string{"cal", "evt.ics"}, ""); err != nil || href != "evt.ics" {
		t.Errorf("explicit href: %q, %v", href, err)
	}
	if href, err := pimHrefArg([]string{"cal"}, "uid-1"); err != nil || href != "by-uid" {
		t.Errorf("uid placeholder: %q, %v", href, err)
	}
	if _, err := pimHrefArg([]string{"cal"}, ""); err == nil {
		t.Error("missing href and uid unexpectedly ok")
	}
}

func TestPimFeedToken(t *testing.T) {
	if tok, err := pimFeedToken("oek_abc"); err != nil || tok != "oek_abc" {
		t.Errorf("bare token: %q, %v", tok, err)
	}
	if tok, err := pimFeedToken("https://api.open.email/api/v1/pim/feeds/oek_xyz"); err != nil || tok != "oek_xyz" {
		t.Errorf("url token: %q, %v", tok, err)
	}
	if _, err := pimFeedToken("https://api.open.email"); err == nil {
		t.Error("token-less URL unexpectedly ok")
	}
}

func pimTestApp(buf *bytes.Buffer) *app {
	return &app{out: &Printer{color: false, out: buf, err: buf}}
}

func TestPrintPimEventsAndContacts(t *testing.T) {
	var buf bytes.Buffer
	a := pimTestApp(&buf)
	events := []coreapi.PimObject{{PimObjectMeta: coreapi.PimObjectMeta{
		Href: "standup.ics", Component: strptr("VEVENT"),
		Dtstart: i64ptr(1751000000), Dtend: i64ptr(1751003600),
		Organizer: strptr("mailto:boss@example.com"), Rrule: strptr("FREQ=WEEKLY"),
		UpdatedAt: 1751000000,
	}}}
	printPimEvents(a, &buf, events)
	out := buf.String()
	for _, want := range []string{"standup.ics", "VEVENT", "boss@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("events output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "mailto:") {
		t.Errorf("mailto: prefix not stripped\n%s", out)
	}

	buf.Reset()
	contacts := []coreapi.PimObject{{PimObjectMeta: coreapi.PimObjectMeta{
		Href: "alice.vcf", VcardFn: strptr("Alice A."), VcardEmail: strptr("alice@example.com"),
		Size: 240, UpdatedAt: 1751000000,
	}}}
	printPimContacts(a, &buf, contacts)
	out = buf.String()
	for _, want := range []string{"alice.vcf", "Alice A.", "alice@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("contacts output missing %q\n%s", want, out)
		}
	}
}

// Expanded listings print one row per occurrence, and a windowed non-recurring
// object still lists once.
func TestPrintPimInstances(t *testing.T) {
	var buf bytes.Buffer
	a := pimTestApp(&buf)
	objs := []coreapi.PimObject{
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "weekly.ics", UID: "u1"},
			Instances: []coreapi.PimInstance{
				{Start: 1751000000, End: 1751003600, Status: strptr("CONFIRMED")},
				{Start: 1751604800, End: 1751608400, Status: strptr("CONFIRMED")},
			}},
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "single.ics", UID: "u2",
			Dtstart: i64ptr(1751100000), Dtend: i64ptr(1751103600)}},
	}
	printPimInstances(a, &buf, objs)
	out := buf.String()
	if strings.Count(out, "weekly.ics") != 2 {
		t.Errorf("want 2 occurrence rows for weekly.ics\n%s", out)
	}
	if strings.Count(out, "single.ics") != 1 {
		t.Errorf("want 1 row for the non-recurring object\n%s", out)
	}
}
