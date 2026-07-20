package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/openemail/openemail-cli/internal/coreapi"
)

func strptr(s string) *string { return &s }
func i64ptr(n int64) *int64   { return &n }

func TestPrintContent(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{color: false, out: &buf, err: &buf}
	c := &coreapi.ContentResult{
		Headers: coreapi.ContentHeaders{
			From:            []coreapi.ContentAddress{{Name: strptr("Acme Billing"), Email: "billing@acme.com"}},
			To:              []coreapi.ContentAddress{{Email: "alice@example.com"}},
			Subject:         strptr("Invoice"),
			Date:            i64ptr(1784332800),
			MessageIDHeader: strptr("<abc@acme.com>"),
		},
		Text: &coreapi.ContentBody{Section: "1", Content: strptr("hello body"), Size: 10, Charset: strptr("utf-8")},
		HTML: &coreapi.ContentBody{Section: "1.2", Size: 2_000_000, Truncated: true},
		Attachments: []coreapi.AttachmentRef{
			{Section: "2", Filename: "invoice.pdf", ContentType: "application/pdf", Size: 35102, Inline: false},
			{Section: "3", Filename: "logo.png", ContentType: "image/png", Size: 800, Inline: true, ContentID: strptr("<logo@x>")},
		},
	}
	printContent(&buf, p, "mbx", "m1", c)
	out := buf.String()

	for _, want := range []string{
		"Acme Billing <billing@acme.com>", // From address formatting
		"alice@example.com",               // To (no name)
		"Invoice",                         // subject
		"<abc@acme.com>",                  // Message-ID
		"hello body",                      // inlined text body
		"truncated",                       // the HTML body is truncated → summary note
		"invoice.pdf",                     // attachment filename
		"logo.png",
		"inline", // the cid image is classified inline
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printContent output missing %q\n---\n%s", want, out)
		}
	}
}

func TestPartFilename(t *testing.T) {
	cases := []struct {
		cd, section, want string
	}{
		{`attachment; filename="invoice.pdf"`, "2", "invoice.pdf"},
		{`attachment; filename*=UTF-8''caf%C3%A9.pdf`, "2", "café.pdf"},
		{`attachment; filename="../../etc/passwd"`, "2", "passwd"}, // path traversal stripped by filepath.Base
		{"", "3.1", "part-3.1"},       // no CD → generated name
		{"attachment", "4", "part-4"}, // CD without filename → generated name
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.cd != "" {
			h.Set("Content-Disposition", tc.cd)
		}
		if got := partFilename(h, tc.section); got != tc.want {
			t.Errorf("partFilename(%q, %q) = %q, want %q", tc.cd, tc.section, got, tc.want)
		}
	}
}
