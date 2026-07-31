package cli

import (
	"strings"
	"testing"
)

func TestParseSendAddress(t *testing.T) {
	cases := []struct {
		in      string
		addr    string
		name    string
		hasName bool
		valid   bool
	}{
		{in: "a@b.com", addr: "a@b.com", valid: true},
		{in: "  a@b.com  ", addr: "a@b.com", valid: true},
		{in: "Alice A <a@b.com>", addr: "a@b.com", name: "Alice A", hasName: true, valid: true},
		{in: `"Alice, A" <a@b.com>`, addr: "a@b.com", name: "Alice, A", hasName: true, valid: true},
		{in: "<a@b.com>", addr: "a@b.com", valid: true},

		{in: "", valid: false},
		{in: "Alice A", valid: false},  // a display name with no address
		{in: "Alice <>", valid: false}, // an empty angle-addr
	}
	for _, tc := range cases {
		got, err := parseSendAddress(tc.in)
		if tc.valid != (err == nil) {
			t.Errorf("parseSendAddress(%q): err=%v, want valid=%v", tc.in, err, tc.valid)
			continue
		}
		if !tc.valid {
			continue
		}
		if got.Address != tc.addr {
			t.Errorf("parseSendAddress(%q).Address = %q, want %q", tc.in, got.Address, tc.addr)
		}
		if tc.hasName {
			if got.Name == nil || *got.Name != tc.name {
				t.Errorf("parseSendAddress(%q).Name = %v, want %q", tc.in, got.Name, tc.name)
			}
		} else if got.Name != nil {
			t.Errorf("parseSendAddress(%q) invented a name %q", tc.in, *got.Name)
		}
	}
}

// A repeated flag and a comma-separated list are both accepted, and a comma
// inside a quoted display name does not split the list.
func TestParseSendAddresses(t *testing.T) {
	got, err := parseSendAddresses([]string{"a@x.com", "b@x.com, c@x.com"})
	if err != nil {
		t.Fatalf("parseSendAddresses: %v", err)
	}
	if len(got) != 3 || got[2].Address != "c@x.com" {
		t.Fatalf("got %+v, want three addresses", got)
	}
	got, err = parseSendAddresses([]string{`"Doe, Jane" <j@x.com>, k@x.com`})
	if err != nil {
		t.Fatalf("quoted list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a comma inside a quoted name must not split: %+v", got)
	}
	if got[0].Name == nil || *got[0].Name != "Doe, Jane" {
		t.Errorf("display name lost: %+v", got[0].Name)
	}
	if _, err := parseSendAddresses([]string{"a@x.com, not an address"}); err == nil {
		t.Error("a bad element should fail the whole list")
	}
}

// The upload's recorded media type must be BARE: core's MIME builder appends
// its own charset and refuses a caller-supplied parameter (invalid_content_type).
func TestAttachmentTypeHasNoParameters(t *testing.T) {
	for _, name := range []string{"report.txt", "notes.html", "data.csv"} {
		got := attachmentType(name)
		if strings.ContainsAny(got, ";\"\\") {
			t.Errorf("attachmentType(%q) = %q, must carry no parameters", name, got)
		}
		if !strings.Contains(got, "/") {
			t.Errorf("attachmentType(%q) = %q, want a type/subtype", name, got)
		}
	}
	if got := attachmentType("report.txt"); got != "text/plain" {
		t.Errorf("attachmentType(report.txt) = %q, want text/plain", got)
	}
	// An unknown extension falls back rather than sending an empty type.
	if got := attachmentType("mystery.zzzz"); got != "application/octet-stream" {
		t.Errorf("unknown extension = %q, want application/octet-stream", got)
	}
	if got := attachmentType("noext"); got != "application/octet-stream" {
		t.Errorf("no extension = %q, want application/octet-stream", got)
	}
}

// An unset body stays nil (omitted) rather than becoming an empty string, but
// an explicitly empty --text is honored.
func TestComposeBody(t *testing.T) {
	got, err := composeBody("", "", false)
	if err != nil || got != nil {
		t.Errorf("unset body = %v, %v; want nil", got, err)
	}
	got, err = composeBody("hello", "", true)
	if err != nil || got == nil || *got != "hello" {
		t.Errorf("inline body = %v, %v", got, err)
	}
	got, err = composeBody("", "", true)
	if err != nil || got == nil || *got != "" {
		t.Errorf("explicit empty --text should be honored, got %v", got)
	}
}

func TestSplitAddressList(t *testing.T) {
	got := splitAddressList("a@x.com, b@x.com ,, c@x.com")
	if len(got) != 3 {
		t.Fatalf("splitAddressList dropped or kept empties wrong: %q", got)
	}
	got = splitAddressList(`"X, Y" <a@x.com>`)
	if len(got) != 1 {
		t.Fatalf("quoted comma split the list: %q", got)
	}
}
