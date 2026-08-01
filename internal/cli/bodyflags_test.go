package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestBodyFlagsHeaders(t *testing.T) {
	b := &bodyFlags{header: []string{"X-Tag: alpha", "  X-Other:beta  "}}
	h, err := b.headers()
	if err != nil {
		t.Fatalf("headers: %v", err)
	}
	if h["X-Tag"] != "alpha" || h["X-Other"] != "beta" {
		t.Errorf("headers = %v", h)
	}
	// No --header at all must yield nil, not an empty map: an empty object in
	// the JSON body is a different thing to send than an omitted field.
	if h, _ := (&bodyFlags{}).headers(); h != nil {
		t.Errorf("no headers must be nil, got %v", h)
	}
}

func TestBodyFlagsHeadersRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"no-colon", ": empty name", "  : x"} {
		if _, err := (&bodyFlags{header: []string{bad}}).headers(); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// A header value may legitimately contain colons (a Message-ID, a URL), so only
// the FIRST colon separates name from value.
func TestBodyFlagsHeaderValueKeepsColons(t *testing.T) {
	h, err := (&bodyFlags{header: []string{"X-Ref: <a@b>; see https://x.test/p"}}).headers()
	if err != nil {
		t.Fatalf("headers: %v", err)
	}
	if h["X-Ref"] != "<a@b>; see https://x.test/p" {
		t.Errorf("value = %q", h["X-Ref"])
	}
}

// bodies() distinguishes "not given" from "given empty": an explicit --text ""
// is a real empty body, while omitting it must leave the field unset so the
// server does not receive an empty string it would have to interpret.
func TestBodyFlagsBodies(t *testing.T) {
	b := &bodyFlags{}
	cmd := &cobra.Command{Use: "x"}
	b.register(cmd)

	text, html, err := b.bodies(cmd, false)
	if err != nil {
		t.Fatalf("bodies: %v", err)
	}
	if text != nil || html != nil {
		t.Errorf("nothing given must yield nil bodies, got %v/%v", text, html)
	}
	// require=true is for the verbs that SEND: an empty message is never what
	// someone meant to submit.
	if _, _, err := b.bodies(cmd, true); err == nil {
		t.Error("a sending verb must refuse an empty body")
	}

	if err := cmd.Flags().Set("text", ""); err != nil {
		t.Fatal(err)
	}
	text, _, err = b.bodies(cmd, true)
	if err != nil {
		t.Fatalf("bodies with explicit empty --text: %v", err)
	}
	if text == nil || *text != "" {
		t.Errorf("an explicit empty --text is a body, got %v", text)
	}
}
