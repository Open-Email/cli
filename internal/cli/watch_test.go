package cli

import "testing"

func TestEventsURL(t *testing.T) {
	cases := []struct {
		api, mbx, want string
	}{
		{"http://localhost:8787", "01ABC", "ws://localhost:8787/api/v1/mailboxes/01ABC/events"},
		{"https://api.open.email", "01ABC", "wss://api.open.email/api/v1/mailboxes/01ABC/events"},
		{"http://localhost:8787/", "01ABC", "ws://localhost:8787/api/v1/mailboxes/01ABC/events"},
	}
	for _, c := range cases {
		a := &app{apiURL: c.api}
		got, err := a.eventsURL(c.mbx)
		if err != nil {
			t.Fatalf("eventsURL(%q): %v", c.api, err)
		}
		if got != c.want {
			t.Errorf("eventsURL(%q,%q)=%q want %q", c.api, c.mbx, got, c.want)
		}
	}
}
