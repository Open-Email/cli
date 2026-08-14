package compose

import (
	"strings"
	"testing"
)

func TestNewDeliveryIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewDeliveryID()
		if len(id) != 26 {
			t.Fatalf("ULID length: got %d (%q)", len(id), id)
		}
		for _, r := range id {
			if !strings.ContainsRune(Crockford, r) {
				t.Fatalf("non-Crockford char %q in %q", r, id)
			}
		}
		// Must never collide with core's reserved delivery-id prefixes or contain '#'.
		if strings.HasPrefix(id, "sent:") || strings.HasPrefix(id, "bounce:") || strings.Contains(id, "#") {
			t.Fatalf("reserved/invalid delivery id: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ULID at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

func TestTextMessageHeaderSanitization(t *testing.T) {
	from := "alice@example.com\r\nBcc: evil@attacker.com"
	to := "bob@example.com\nCC: hacker@attacker.com"
	subj := "Subject Line\r\nInjected-Header: yes"
	body := "Hello World"

	msg := string(TextMessage(from, to, subj, body))

	// Ensure injected lines were stripped/neutralized from header block
	parts := strings.Split(msg, "\r\n\r\n")
	if len(parts) < 2 {
		t.Fatalf("malformed message format: %q", msg)
	}
	headers := parts[0]
	if strings.Contains(headers, "evil@attacker.com") {
		t.Errorf("expected header injection to be stripped, got: %s", headers)
	}
	if strings.Contains(headers, "hacker@attacker.com") {
		t.Errorf("expected CC injection to be stripped, got: %s", headers)
	}
	if strings.Contains(headers, "Injected-Header: yes") {
		t.Errorf("expected Subject injection to be stripped, got: %s", headers)
	}
}
