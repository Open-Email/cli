package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/secrets"
	"github.com/spf13/cobra"
)

func TestLogoutIgnoresAmbientEnvTokenWhenNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_PROFILE", "")
	t.Setenv("OPENEMAIL_API_URL", "")
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	t.Setenv("OPENEMAIL_API_KEY", "oek_from_env")

	a := &app{}
	if err := a.preRun(); err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	a.out = &Printer{out: &bytes.Buffer{}, err: &errBuf}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := a.runLogout(cmd, false); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	if !strings.Contains(errBuf.String(), "Not logged in") {
		t.Fatalf("expected 'Not logged in' message when profile has no stored credentials, got:\n%s", errBuf.String())
	}
}

func TestLogoutUsesStoredProfileTokenAndURLAndClearsState(t *testing.T) {
	var mu sync.Mutex
	var profAuthHeader string
	var profRevokedKey string
	profSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		profAuthHeader = r.Header.Get("Authorization")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		profRevokedKey = parts[len(parts)-1]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"revoked": true})
	}))
	defer profSrv.Close()

	foreignHit := false
	foreignSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		foreignHit = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer foreignSrv.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_PROFILE", "")
	t.Setenv("OPENEMAIL_API_URL", "")
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	// Set an ambient env token and a foreign --api-url to prove logout uses the
	// profile's own stored token and APIURL.
	t.Setenv("OPENEMAIL_API_KEY", "oek_ambient_env")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := secrets.Save(cfg.ConfigDir(), "default", "oek_stored_profile", true); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetProfile("default", config.Profile{
		APIURL:         profSrv.URL,
		ConsoleURL:     "https://console.prof.test",
		AccountID:      "acc_1",
		Role:           "account",
		KeyStorage:     secrets.File,
		KeyID:          "key_prof_1",
		KeyName:        "laptop",
		DefaultMailbox: "mbx_stale",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	a := &app{flagAPIURL: foreignSrv.URL}
	if err := a.preRun(); err != nil {
		t.Fatal(err)
	}
	a.out = quietPrinter()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := a.runLogout(cmd, false); err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	mu.Lock()
	gotForeign := foreignHit
	gotAuth := profAuthHeader
	gotKey := profRevokedKey
	mu.Unlock()

	if gotForeign {
		t.Fatal("logout contacted foreign --api-url instead of profile's APIURL")
	}
	if gotKey != "key_prof_1" {
		t.Fatalf("revoked key %q on profile server, want key_prof_1", gotKey)
	}
	if gotAuth != "Bearer oek_stored_profile" {
		t.Fatalf("Authorization header = %q, want 'Bearer oek_stored_profile'", gotAuth)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	prof, _ := reloaded.Profile("default")
	if prof.DefaultMailbox != "" {
		t.Fatalf("DefaultMailbox %q was not cleared on logout", prof.DefaultMailbox)
	}
}

// logoutAgainst logs out a CLI-minted account profile whose revoke call core
// answers with status, and returns what the user was told.
func logoutAgainst(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": http.StatusText(status)})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_PROFILE", "")
	t.Setenv("OPENEMAIL_API_URL", "")
	t.Setenv("OPENEMAIL_API_KEY", "")
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := secrets.Save(cfg.ConfigDir(), "default", "oek_stored", true); err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("default", config.Profile{
		APIURL: srv.URL, AccountID: "acc_1", Role: "account",
		KeyStorage: secrets.File, KeyID: "key_self",
	})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	a := &app{}
	if err := a.preRun(); err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	a.out = &Printer{out: &bytes.Buffer{}, err: &errBuf}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := a.runLogout(cmd, false); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	return errBuf.String()
}

// `keys revoke` on your own key hints `openemail logout`, and the revoke
// that follows is answered 401: the key is already dead. Neither "could not
// revoke" nor "not minted by the CLI" is true of it.
func TestLogoutAfterOwnKeyRevokedSaysNothingFalse(t *testing.T) {
	got := logoutAgainst(t, http.StatusUnauthorized)
	if strings.Contains(got, "could not revoke") || strings.Contains(got, "not minted by the CLI") {
		t.Fatalf("logout over a dead key: %q", got)
	}
}

// A CLI-minted key that could not be revoked (an outage) is still live, and
// it WAS minted by the CLI: say which key to revoke and where.
func TestLogoutWhenRevokeFailsNamesTheKey(t *testing.T) {
	got := logoutAgainst(t, http.StatusInternalServerError)
	if strings.Contains(got, "not minted by the CLI") || !strings.Contains(got, "key_self") || !strings.Contains(got, "console") {
		t.Fatalf("logout after a failed revoke: %q", got)
	}
}
