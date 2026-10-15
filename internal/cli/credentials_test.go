package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCredentialsListShowsExpiredStatus(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"credentials": []map[string]any{
				{
					"id":        "cred_expired",
					"kind":      "app_password",
					"username":  "alice@example.com",
					"createdAt": past - 3600,
					"expiresAt": past,
				},
			},
		})
	}))
	defer srv.Close()

	var outBuf bytes.Buffer
	a := &app{
		out:         &Printer{out: &outBuf, err: &bytes.Buffer{}},
		apiURL:      srv.URL,
		token:       "oek_test",
		tokenSource: "flag",
	}
	cmd := newCredentialListCmd(a)
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"01J00000000000000000000MBX"}); err != nil {
		t.Fatalf("credentials list: %v", err)
	}
	if !strings.Contains(outBuf.String(), "expired") {
		t.Fatalf("expected 'expired' status in table output, got:\n%s", outBuf.String())
	}
}

func TestCredentialsCreateValidatesFlagsBeforeAuth(t *testing.T) {
	// Notice a.token is empty (not authenticated). Invalid flags must still fail
	// with usageError (exit code 2) before checking auth (exit code 4).
	a := &app{
		out:         quietPrinter(),
		profileName: "default",
		apiURL:      "https://api.open.email",
	}
	cmd := newCredentialCreateCmd(a)
	cmd.SetContext(context.Background())
	_ = cmd.Flags().Set("kind", "not-a-kind")
	err := cmd.RunE(cmd, []string{"01J00000000000000000000MBX"})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("expected usageError (code 2) before auth check, got %v", err)
	}
}
