package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAccountCore records the body of the last account PATCH and answers with a
// minimal account row.
func fakeAccountCore(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	body := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ACC1","name":"Acme"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

// The create-velocity flags carry the send caps' three states onto the wire:
// a number, 'unlimited' as 0, and 'default' as an explicit null that drops the
// override. Null and 0 are opposite answers on a cap, so both are pinned.
func TestAccountUpdateCreateVelocityFlags(t *testing.T) {
	srv, body := fakeAccountCore(t)
	cmd := newAccountUpdateCmd(domainTestApp(srv.URL))
	cmd.SetArgs([]string{"ACC1",
		"--mailbox-creates-per-day", "200",
		"--address-creates-per-day", "unlimited",
		"--domain-creates-per-day", "default",
	})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}
	b := *body
	if got := b["mailboxCreatesPerDay"]; got != float64(200) {
		t.Errorf("mailboxCreatesPerDay = %v; want 200", got)
	}
	if got := b["addressCreatesPerDay"]; got != float64(0) {
		t.Errorf("addressCreatesPerDay = %v; want 0 (unlimited)", got)
	}
	if got, ok := b["domainCreatesPerDay"]; !ok || got != nil {
		t.Errorf("domainCreatesPerDay = %v (present=%v); want an explicit null", got, ok)
	}
	if len(b) != 3 {
		t.Errorf("only the flags passed may reach the wire, got %v", b)
	}
}

// Five flags share one parser, so a bad value must say which flag it was.
func TestAccountUpdateCreateVelocityNamesTheBadFlag(t *testing.T) {
	cmd := newAccountUpdateCmd(domainTestApp("http://127.0.0.1:0"))
	cmd.SetArgs([]string{"ACC1", "--domain-creates-per-day", "lots"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--domain-creates-per-day") {
		t.Fatalf("error = %v; want one naming --domain-creates-per-day", err)
	}
}

// The hourly send caps, the receive allowance, the forward cap and the account's
// defaults for its mailboxes' send caps (core #555) ride the same three states.
// `--recv-bytes-per-day` takes a human size like the storage pool does.
func TestAccountUpdateVolumeAndMailboxDefaultFlags(t *testing.T) {
	srv, body := fakeAccountCore(t)
	cmd := newAccountUpdateCmd(domainTestApp(srv.URL))
	cmd.SetArgs([]string{"ACC1",
		"--send-msgs-per-hour", "1500",
		"--send-rcpts-per-hour", "6000",
		"--recv-msgs-per-day", "450000",
		"--recv-bytes-per-day", "100G",
		"--forward-msgs-per-day", "50000",
		"--mailbox-send-msgs-per-day", "2000",
		"--mailbox-send-rcpts-per-day", "8000",
		"--mailbox-send-msgs-per-hour", "unlimited",
		"--mailbox-send-rcpts-per-hour", "default",
	})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}
	b := *body
	for field, want := range map[string]float64{
		"sendMsgsPerHour": 1500, "sendRcptsPerHour": 6000, "recvMsgsPerDay": 450000,
		"recvBytesPerDay": 100 * 1024 * 1024 * 1024, "forwardMsgsPerDay": 50000,
		"mailboxSendMsgsPerDay": 2000, "mailboxSendRcptsPerDay": 8000,
		"mailboxSendMsgsPerHour": 0,
	} {
		if got := b[field]; got != want {
			t.Errorf("%s = %v; want %v", field, got, want)
		}
	}
	if got, ok := b["mailboxSendRcptsPerHour"]; !ok || got != nil {
		t.Errorf("mailboxSendRcptsPerHour = %v (present=%v); want an explicit null", got, ok)
	}
	if len(b) != 9 {
		t.Errorf("only the flags passed may reach the wire, got %v", b)
	}
}

func TestAccountUpdateNamesTheBadMailboxDefaultFlag(t *testing.T) {
	cmd := newAccountUpdateCmd(domainTestApp("http://127.0.0.1:0"))
	cmd.SetArgs([]string{"ACC1", "--mailbox-send-msgs-per-hour", "lots"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--mailbox-send-msgs-per-hour") {
		t.Fatalf("error = %v; want one naming --mailbox-send-msgs-per-hour", err)
	}
	cmd = newAccountUpdateCmd(domainTestApp("http://127.0.0.1:0"))
	cmd.SetArgs([]string{"ACC1", "--recv-bytes-per-day", "lots"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--recv-bytes-per-day") {
		t.Fatalf("error = %v; want one naming --recv-bytes-per-day", err)
	}
}
